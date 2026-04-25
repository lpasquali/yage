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
	"sort"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// Restore ports kind_bootstrap_state_restore.
//
// TODO: restore still drives `kubectl --context X apply -f -` on each cleaned
// JSON document. Migrating to internal/k8sclient.Client.ApplyUnstructured
// would remove the kubectl dependency, but the kubectl path also benefits
// from kubectl's apply semantics (last-applied-configuration handling, CRD
// readiness retries, etc.) that we'd have to reproduce. Keeping the
// shell-out is the pragmatic choice until backup is also migrated.
//
// Steps:
//  1. require kubectl and an archive path that exists.
//  2. resolve kind-<KIND_CLUSTER_NAME> context.
//  3. decrypt archive based on suffix (.age -> age, .enc -> openssl),
//     otherwise assume plain .tar[.gz]; extract to a tmp dir.
//  4. walk data/<ns>/ in sorted order; for each namespace.json and every
//     line in objects.jsonl, strip status/metadata churn fields, then
//     kubectl apply to the target context.
func Restore(cfg *config.Config, src string) error {
	if !shell.CommandExists("kubectl") {
		logx.Err("kubectl required for kind_bootstrap_state_restore")
		return fmt.Errorf("kubectl missing")
	}
	if src == "" {
		logx.Err("Usage: pass backup archive path (e.g. bootstrap-kind-backup-*.tar.gz, *.tar.gz.age, *.tar.gz.enc)")
		return fmt.Errorf("no src")
	}
	if _, err := os.Stat(src); err != nil {
		logx.Err("Usage: pass backup archive path (e.g. bootstrap-kind-backup-*.tar.gz, *.tar.gz.age, *.tar.gz.enc)")
		return err
	}
	abs := src
	if !filepath.IsAbs(abs) {
		cwd, _ := os.Getwd()
		abs = filepath.Join(cwd, abs)
	}
	ctx := "kind-" + cfg.KindClusterName
	if !kubeContextExists(ctx) {
		logx.Err("kube context %s not found", ctx)
		return fmt.Errorf("no context")
	}

	tmp, err := os.MkdirTemp(os.Getenv("TMPDIR"), "bkr.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	passphrase := cfg.BootstrapKindBackupPassphrase
	if passphrase == "" {
		passphrase = os.Getenv("AGE_PASSPHRASE")
	}

	switch {
	case strings.HasSuffix(abs, ".age"):
		if passphrase == "" {
			logx.Err("Set BOOTSTRAP_KIND_BACKUP_PASSPHRASE or AGE_PASSPHRASE to decrypt")
			return fmt.Errorf("no passphrase")
		}
		_ = os.Setenv("AGE_PASSPHRASE", passphrase)
		if err := extractViaPipe(tmp,
			[]string{"age", "-d", "-o", "-", abs},
			true /* gunzip */); err != nil {
			return err
		}
	case strings.HasSuffix(abs, ".enc"):
		if passphrase == "" {
			logx.Err("Set BOOTSTRAP_KIND_BACKUP_PASSPHRASE to decrypt")
			return fmt.Errorf("no passphrase")
		}
		_ = os.Setenv("BOOTSTRAP_KIND_BACKUP_PASSPHRASE", passphrase)
		if err := extractViaPipe(tmp, []string{
			"openssl", "enc", "-aes-256-cbc", "-d", "-pbkdf2",
			"-pass", "env:BOOTSTRAP_KIND_BACKUP_PASSPHRASE",
			"-in", abs,
		}, true /* gunzip */); err != nil {
			return err
		}
	default:
		if err := extractFile(abs, tmp); err != nil {
			return err
		}
	}

	if _, err := os.Stat(filepath.Join(tmp, "data")); err != nil {
		logx.Err("Invalid backup archive: missing data/ after extract")
		return fmt.Errorf("bad archive")
	}
	if _, err := os.Stat(filepath.Join(tmp, "kind")); err == nil {
		logx.Log("This archive includes kind/ (kind-config.yaml, README) — use it to recreate the management kind cluster; see kind/README in the extracted tree or tarball.")
	}

	if err := applyRestoreTree(ctx, tmp); err != nil {
		return err
	}
	logx.Log("kind_bootstrap_state_restore: applied from %s into %s", abs, ctx)
	return nil
}

// extractFile opens abs on disk, gunzips if needed, then untars to dest.
func extractFile(abs, dest string) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(abs, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gr.Close()
		r = gr
	}
	return untar(r, dest)
}

// extractViaPipe runs argv to produce the (compressed) tar on stdout, then
// optionally gunzips, then untars into dest. Used for the age/openssl paths.
func extractViaPipe(dest string, argv []string, gunzip bool) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var r io.Reader = stdout
	if gunzip {
		gr, err := gzip.NewReader(stdout)
		if err != nil {
			_ = cmd.Wait()
			return err
		}
		defer gr.Close()
		r = gr
	}
	if err := untar(r, dest); err != nil {
		_ = cmd.Wait()
		return err
	}
	return cmd.Wait()
}

func untar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Guard against path traversal via entries containing ".." segments.
		target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
		rel, err := filepath.Rel(dest, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("archive entry escapes dest: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		default:
			// ignore symlinks / other types — bash tar -x handles them but
			// the backup writer never creates them.
		}
	}
}

// applyRestoreTree iterates data/<ns>/, cleaning+applying namespace.json
// first and every line of objects.jsonl after.
func applyRestoreTree(ctx, root string) error {
	data := filepath.Join(root, "data")
	entries, err := os.ReadDir(data)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, ns := range names {
		nd := filepath.Join(data, ns)
		if njp := filepath.Join(nd, "namespace.json"); fileExists(njp) {
			b, err := os.ReadFile(njp)
			if err != nil {
				return err
			}
			if err := cleanAndApply(ctx, b); err != nil {
				return err
			}
		}
		jlp := filepath.Join(nd, "objects.jsonl")
		if !fileExists(jlp) {
			continue
		}
		jf, err := os.Open(jlp)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(jf)
		for {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				if err == io.EOF {
					break
				}
				fmt.Fprintln(os.Stderr, "skip line:", err)
				continue
			}
			if len(strings.TrimSpace(string(raw))) == 0 {
				continue
			}
			if err := cleanAndApply(ctx, raw); err != nil {
				jf.Close()
				return err
			}
		}
		jf.Close()
	}
	return nil
}

// cleanAndApply strips status / managedFields / cluster-metadata fields
// that kubectl apply can't round-trip, then runs `kubectl --context ctx
// apply -f -` with the resulting JSON on stdin. Matches the bash inline
// Python exactly (L2524-L2539).
func cleanAndApply(ctx string, raw []byte) error {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil // skip malformed, bash prints "skip line:" and continues
	}
	delete(obj, "status")
	if m, ok := obj["metadata"].(map[string]any); ok {
		for _, k := range []string{
			"resourceVersion", "uid", "selfLink", "generation", "creationTimestamp",
			"deletionTimestamp", "deletionGracePeriodSeconds", "managedFields", "ownerReferences",
		} {
			delete(m, k)
		}
		if a, ok := m["annotations"].(map[string]any); ok {
			delete(a, "kubectl.kubernetes.io/last-applied-configuration")
			delete(a, "deployment.kubernetes.io/revision")
			if len(a) == 0 {
				delete(m, "annotations")
			}
		}
	}
	doc, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return shell.Pipe(string(doc), "kubectl", "--context", ctx, "apply", "-f", "-")
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
