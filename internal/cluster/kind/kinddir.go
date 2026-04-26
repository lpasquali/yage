package kind

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// writeKindDir ports kind_bootstrap_state_backup_write_kind_dir.
// Creates <tmp>/kind/{kind-config.yaml, meta.json, README} so the archive
// has everything needed to recreate the kind cluster on a fresh host.
//
// kind-config.yaml is taken from:
//  1. cfg.KindConfig when that file exists on disk ("KIND_CONFIG_file"),
//  2. otherwise rebuilt from `docker ps -a ... label=io.x-k8s.kind.cluster=<name>`
//     ("docker_nodes"),
//  3. otherwise a minimal single-control-plane fallback ("fallback_minimal").
func writeKindDir(cfg *config.Config, tmp string) error {
	if tmp == "" {
		return nil
	}
	name := strings.TrimSpace(cfg.KindClusterName)
	if name == "" {
		logx.Warn("KIND_CLUSTER_NAME unset — skipping kind/ recipe in backup")
		return nil
	}
	kdir := filepath.Join(tmp, "kind")
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		return err
	}

	meta := map[string]any{
		"kind_cluster_name": name,
		"config_source":     "unknown",
	}

	if cfg.KindConfig != "" {
		if raw, err := os.ReadFile(cfg.KindConfig); err == nil {
			if err := os.WriteFile(filepath.Join(kdir, "kind-config.yaml"), raw, 0o644); err != nil {
				return err
			}
			meta["config_source"] = "KIND_CONFIG_file"
			meta["config_path"] = cfg.KindConfig
		}
	}
	if meta["config_source"] == "unknown" {
		// Rebuild from docker node labels.
		out, _, err := shell.Capture(
			"docker", "ps", "-a",
			"--filter", "label=io.x-k8s.kind.cluster="+name,
			"--format", `{{.Label "io.x-k8s.kind.role"}}`+"\t"+`{{.Image}}`+"\t"+`{{.Names}}`,
		)
		if err != nil {
			meta["docker_error"] = truncate(err.Error(), 200)
		}
		var lines []string
		for _, ln := range strings.Split(out, "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" || !strings.Contains(ln, "\t") {
				continue
			}
			parts := strings.SplitN(ln, "\t", 3)
			if len(parts) >= 2 {
				lines = append(lines, ln)
			}
		}
		if len(lines) > 0 {
			// control-plane first, then workers — stable sort to match bash.
			sort.SliceStable(lines, func(i, j int) bool {
				ri := strings.SplitN(lines[i], "\t", 2)[0]
				rj := strings.SplitN(lines[j], "\t", 2)[0]
				a := 1
				if ri == "control-plane" {
					a = 0
				}
				b := 1
				if rj == "control-plane" {
					b = 0
				}
				if a != b {
					return a < b
				}
				return lines[i] < lines[j]
			})
			var y strings.Builder
			y.WriteString("kind: Cluster\n")
			y.WriteString("apiVersion: kind.x-k8s.io/v1alpha4\n")
			y.WriteString("nodes:\n")
			for _, ln := range lines {
				p := strings.SplitN(ln, "\t", 3)
				role, image := p[0], p[1]
				fmt.Fprintf(&y, "- role: %s\n  image: %s\n", role, image)
			}
			if err := os.WriteFile(filepath.Join(kdir, "kind-config.yaml"), []byte(y.String()), 0o644); err != nil {
				return err
			}
			meta["config_source"] = "docker_nodes"
		} else {
			fallback := "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n- role: control-plane\n"
			if err := os.WriteFile(filepath.Join(kdir, "kind-config.yaml"), []byte(fallback), 0o644); err != nil {
				return err
			}
			meta["config_source"] = "fallback_minimal"
		}
	}

	// Best-effort: record the kind CLI version if a binary happens to be on
	// PATH. With the kind library embedded in this binary the CLI is no
	// longer required, so a missing `kind` is expected and not an error.
	// TODO: when the cluster lifecycle wrappers in kind.go land, replace
	// this with `sigs.k8s.io/kind/pkg/cmd/kind/version.DisplayVersion()`
	// so the meta is populated even when the CLI is absent.
	if kv, _, err := shell.Capture("kind", "version"); err == nil {
		if kv = strings.TrimSpace(kv); kv != "" {
			meta["kind_cli"] = truncate(kv, 2000)
		}
	}

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(kdir, "meta.json"), append(metaBytes, '\n'), 0o644); err != nil {
		return err
	}

	readme := fmt.Sprintf(`# Recreate kind management cluster

The management cluster is Docker-backed and does not store the original kind
config. This bundle is either a copy of KIND_CONFIG (if that file was set when
the backup ran) or a rebuild from `+"`docker`"+` (node `+"`image`"+` + `+"`role`"+` labels).

1. Install kind + Docker as usual, then from the directory that contains
   `+"`kind/kind-config.yaml`"+` (e.g. after `+"`tar -xzf`"+` your backup):
   `+"```bash\n"+`   kind create cluster --name %s --config kind/kind-config.yaml
   `+"```"+`
2. Re-merge kubeconfig if needed: `+"`kind export kubeconfig --name %s`"+`
3. Then restore the namespaced data (e.g. `+"`kind_bootstrap_state_restore`"+` from
   this script, or `+"`kubectl apply`"+` the `+"`data/`"+` tree) against context
   `+"`kind-%s`"+`.
`, name, name, name)
	if err := os.WriteFile(filepath.Join(kdir, "README"), []byte(readme), 0o644); err != nil {
		return err
	}

	logx.Log("Kind backup: wrote %s/kind/ (config + README to recreate the cluster)", tmp)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
