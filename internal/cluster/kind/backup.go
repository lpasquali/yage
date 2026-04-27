// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kind

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// Backup ports kind_bootstrap_state_backup.
//
// TODO: backup is intentionally still implemented via `kubectl get ... -o json`
// shell-outs. Re-implementing it on top of the typed/dynamic client in
// internal/k8sclient would be straightforward, but the resulting tarball
// layout (bytes inside data/<ns>/objects.jsonl in particular) MUST stay
// byte-compatible with the bash-produced format so existing backups remain
// restorable. Reaching that compatibility bar against arbitrary
// CRDs+namespace contents is high-risk; the kubectl-based path is the
// canonical reference. Migrate when there is a comprehensive round-trip test.
//
// Steps:
//  1. require kubectl on PATH and the kind-<KIND_CLUSTER_NAME> context to exist.
//  2. resolve destination path (absolute; default "bootstrap-kind-backup-<ts>.tar")
//  3. for each namespace from BackupNamespaces(cfg):
//     - write tmp/data/<ns>/namespace.json
//     - list namespaced api-resources, get each, write objects to tmp/data/<ns>/objects.jsonl
//     - write tmp/data/<ns>/meta.json
//  4. writeKindDir(cfg, tmp) drops in tmp/kind/{kind-config.yaml,meta.json,README}
//  5. resolve encryption mode (auto -> age|openssl|none depending on tools + passphrase)
//  6. stream tar of (namespaces.lst, data/, kind/) -> gzip(1) -> optional encrypt -> dest
func Backup(cfg *config.Config, outPath string) error {
	if !shell.CommandExists("kubectl") {
		logx.Err("kubectl required for kind_bootstrap_state_backup")
		return fmt.Errorf("kubectl missing")
	}
	ctx := "kind-" + cfg.KindClusterName
	if !kubeContextExists(ctx) {
		logx.Err("kube context %s not found", ctx)
		return fmt.Errorf("no context")
	}

	dest := outPath
	if dest == "" {
		dest = cfg.BootstrapKindBackupOut
	}
	if dest == "" {
		dest = "bootstrap-kind-backup-" + time.Now().Format("20060102-150405") + ".tar"
	}
	abs := dest
	if !filepath.IsAbs(abs) {
		cwd, _ := os.Getwd()
		abs = filepath.Join(cwd, abs)
	}

	tmp, err := os.MkdirTemp(os.Getenv("TMPDIR"), "bkp.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "data"), 0o755); err != nil {
		return err
	}

	nsListPath := filepath.Join(tmp, "namespaces.lst")
	if err := os.WriteFile(nsListPath, nil, 0o644); err != nil {
		return err
	}

	for _, ns := range BackupNamespaces(cfg) {
		if ns == "" {
			continue
		}
		if err := appendLine(nsListPath, ns); err != nil {
			return err
		}
		if err := backupNamespace(ctx, ns, tmp); err != nil {
			logx.Err("backup failed for namespace %s: %v", ns, err)
			return err
		}
	}

	if fi, err := os.Stat(nsListPath); err != nil || fi.Size() == 0 {
		logx.Err("No namespaces to backup (set BOOTSTRAP_KIND_BACKUP_NAMESPACES?)")
		return fmt.Errorf("no namespaces")
	}

	if err := writeKindDir(cfg, tmp); err != nil {
		return err
	}

	passphrase := cfg.BootstrapKindBackupPassphrase
	if passphrase == "" {
		passphrase = os.Getenv("AGE_PASSPHRASE")
	}
	want := cfg.BootstrapKindBackupEncrypt
	if want == "" {
		want = "auto"
	}
	if want == "auto" {
		switch {
		case passphrase != "" && shell.CommandExists("age"):
			want = "age"
		case passphrase != "" && shell.CommandExists("openssl"):
			want = "openssl"
		default:
			want = "none"
		}
	}
	if want != "none" && passphrase == "" {
		logx.Warn("BOOTSTRAP_KIND_BACKUP_ENCRYPT is %s but no passphrase set — writing unencrypted tar (set BOOTSTRAP_KIND_BACKUP_PASSPHRASE or AGE_PASSPHRASE, or set BOOTSTRAP_KIND_BACKUP_ENCRYPT=none)", want)
		want = "none"
	}

	// Writer chain: tar -> gzip(1) -> optional encrypt -> file
	var outPathFinal string
	switch want {
	case "none":
		outPathFinal = abs + ".gz"
		if err := writeArchive(tmp, outPathFinal, nil); err != nil {
			return err
		}
		// bash removes $abs after writing $abs.gz — harmless if it doesn't exist
		_ = os.Remove(abs)
		logx.Log("Wrote %s (%s) — encrypt: none", outPathFinal, humanSize(outPathFinal))
	case "age":
		outPathFinal = abs + ".gz.age"
		if err := writeArchive(tmp, outPathFinal, []string{"age", "-e", "-o", outPathFinal}); err != nil {
			return err
		}
		logx.Log("Wrote %s (%s) — age", outPathFinal, humanSize(outPathFinal))
	default: // openssl
		outPathFinal = abs + ".gz.enc"
		if err := writeArchive(tmp, outPathFinal, []string{
			"openssl", "enc", "-aes-256-cbc", "-pbkdf2", "-salt",
			"-pass", "env:BOOTSTRAP_KIND_BACKUP_PASSPHRASE",
			"-out", outPathFinal,
		}); err != nil {
			return err
		}
		logx.Log("Wrote %s (%s) — openssl", outPathFinal, humanSize(outPathFinal))
	}
	return nil
}

// backupNamespace writes data/<ns>/namespace.json + objects.jsonl
// + meta.json.
func backupNamespace(ctx, ns, tmp string) error {
	odir := filepath.Join(tmp, "data", ns)
	if err := os.MkdirAll(odir, 0o755); err != nil {
		return err
	}
	// namespace.json — ignore errors; only write on rc=0.
	if out, _, err := shell.Capture("kubectl", "--context", ctx, "get", "namespace", ns, "-o", "json"); err == nil && strings.TrimSpace(out) != "" {
		if err := os.WriteFile(filepath.Join(odir, "namespace.json"), []byte(out), 0o644); err != nil {
			return err
		}
	}
	// api-resources list
	resources, _, err := shell.Capture("kubectl", "--context", ctx, "api-resources",
		"--verbs=list", "--namespaced", "-o", "name")
	if err != nil || strings.TrimSpace(resources) == "" {
		fmt.Fprintln(os.Stderr, "api-resources list failed; skipping object dump")
	}
	jlp := filepath.Join(odir, "objects.jsonl")
	jf, err := os.Create(jlp)
	if err != nil {
		return err
	}
	defer jf.Close()
	enc := json.NewEncoder(jf)
	enc.SetEscapeHTML(false)
	nObj := 0
	for _, gvr := range strings.Split(resources, "\n") {
		gvr = strings.TrimSpace(gvr)
		if gvr == "" || gvr == "events" {
			continue
		}
		out, _, err := shell.Capture("kubectl", "--context", ctx, "get", gvr,
			"-n", ns, "-o", "json", "--request-timeout=5m")
		if err != nil || strings.TrimSpace(out) == "" {
			continue
		}
		var doc struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			continue
		}
		for _, item := range doc.Items {
			var v any
			if err := json.Unmarshal(item, &v); err != nil {
				continue
			}
			if err := enc.Encode(v); err != nil {
				return err
			}
			nObj++
		}
	}

	metaPath := filepath.Join(odir, "meta.json")
	meta := map[string]any{
		"version":   1,
		"context":   ctx,
		"namespace": ns,
		"ts":        time.Now().Unix(),
		"objects":   nObj,
	}
	metaBytes, err := json.MarshalIndent(meta, "", "")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, append(metaBytes, '\n'), 0o644)
}

// writeArchive streams tar(namespaces.lst + data/ + kind/) through gzip(1)
// into `dest` directly, or through an exec.Cmd stage when `encryptArgv` is
// non-nil (the child is expected to read stdin and write to `dest` itself
// via its -o/-out flag).
func writeArchive(tmp, dest string, encryptArgv []string) error {
	var writer io.WriteCloser
	var cmd *exec.Cmd
	if encryptArgv == nil {
		f, err := os.Create(dest)
		if err != nil {
			return err
		}
		writer = f
	} else {
		cmd = exec.Command(encryptArgv[0], encryptArgv[1:]...)
		// age reads AGE_PASSPHRASE from env; openssl reads the named var.
		// The orchestrator puts BOOTSTRAP_KIND_BACKUP_PASSPHRASE / AGE_PASSPHRASE
		// into os.Environ() before starting us, so Env inheritance is enough.
		cmd.Env = os.Environ()
		cmd.Stderr = os.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		writer = stdin
		if err := cmd.Start(); err != nil {
			return err
		}
	}

	gz, err := gzip.NewWriterLevel(writer, gzip.BestSpeed) // -1
	if err != nil {
		writer.Close()
		return err
	}
	tw := tar.NewWriter(gz)

	// tar the three top-level entries from tmp, matching bash's
	// `tar -cf - namespaces.lst data kind` cwd'd at tmp.
	for _, top := range []string{"namespaces.lst", "data", "kind"} {
		abs := filepath.Join(tmp, top)
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		if err := tarAddTree(tw, tmp, abs); err != nil {
			tw.Close()
			gz.Close()
			writer.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if cmd != nil {
		return cmd.Wait()
	}
	return nil
}

// tarAddTree adds `path` (file or directory, recursive) to the tar writer,
// using names relative to `base` — same layout as bash's tar.
func tarAddTree(tw *tar.Writer, base, path string) error {
	return filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		// Normalize to forward-slashes (tar convention).
		rel = filepath.ToSlash(rel)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

func kubeContextExists(ctx string) bool {
	out, _, _ := shell.Capture("kubectl", "config", "get-contexts", "-o", "name")
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if ln == ctx {
			return true
		}
	}
	return false
}

func humanSize(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "?"
	}
	n := fi.Size()
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}