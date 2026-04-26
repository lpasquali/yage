package orchestrator

import (
	"os"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// minimalKindConfig is the YAML written when no KIND_CONFIG is set.
// Matches the bash heredoc (L4311-L4316).
const minimalKindConfig = `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
`

// EnsureKindConfig ports bootstrap_ensure_kind_config (L4304-L4320).
// Dies when cfg.KindConfig is set but the file is missing. Otherwise
// creates an ephemeral kind-config under $TMPDIR, registers it for
// cleanup, and stores the path on cfg.KindConfig.
func EnsureKindConfig(cfg *config.Config) {
	if cfg.KindConfig != "" {
		shell.RequireFile(cfg.KindConfig)
		return
	}
	RegisterExitTrap()
	f, err := os.CreateTemp("", "yage-kind.*.yaml")
	if err != nil {
		logx.Die("Cannot create ephemeral kind config: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(minimalKindConfig); err != nil {
		logx.Die("Cannot write ephemeral kind config: %v", err)
	}
	cfg.KindConfig = f.Name()
	cfg.BootstrapEphemeralKindConfig = f.Name()
	cfg.BootstrapKindConfigEphemeral = true
	SetEphemeralKindConfig(f.Name())
	logx.Log("Using ephemeral kind config %s (set KIND_CONFIG or --kind-config to use a file on disk).", cfg.KindConfig)
}
