package orchestrator

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// ExitCleanup tracks ephemeral files created by the bootstrap so that
// bootstrap_exit_cleanup_all can remove them on process exit. Matches
// the bash BOOTSTRAP_*_EPHEMERAL globals + `trap bootstrap_exit_cleanup_all EXIT`.
//
// Go doesn't have a real EXIT trap — deferred functions don't run on
// os.Exit (which logx.Die calls). To keep parity we:
//
//   - Install a SIGINT / SIGTERM handler that runs cleanup once, then
//     propagates the signal to the default handler.
//   - Expect main() to call Cleanup() explicitly on both success and
//     failure paths.
//
// Callers never read this struct directly; the package-global var
// defaultCleanup is the single registry.
type ExitCleanup struct {
	once               sync.Once
	ephemeralKindCfg   string
	ephemeralCAPI      string
	ephemeralClusterCt string
}

var defaultCleanup = &ExitCleanup{}

// RegisterExitTrap ports bootstrap_register_exit_trap. Idempotent; the
// first call installs a signal handler.
func RegisterExitTrap() {
	defaultCleanup.once.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-ch
			defaultCleanup.run()
			// Let the process die with a conventional exit code — matches
			// the bash trap behaviour (cleanup, then propagate).
			os.Exit(130)
		}()
	})
}

// Cleanup runs the registered cleanup immediately. Safe to call many
// times — subsequent calls are no-ops after a successful first call.
func Cleanup() { defaultCleanup.run() }

// Ephemeral-file setters called from bootstrap_ensure_kind_config /
// bootstrap_ensure_capi_manifest_path / bootstrap_sync_clusterctl_config_file.
func SetEphemeralKindConfig(path string)       { defaultCleanup.ephemeralKindCfg = path }
func SetEphemeralCAPIManifest(path string)     { defaultCleanup.ephemeralCAPI = path }
func SetEphemeralClusterctlConfig(path string) { defaultCleanup.ephemeralClusterCt = path }

// ClearEphemeralClusterctlConfig forgets any registered temp clusterctl
// config path so a caller can replace it with a new one.
func ClearEphemeralClusterctlConfig() { defaultCleanup.ephemeralClusterCt = "" }

func (e *ExitCleanup) run() {
	if e.ephemeralClusterCt != "" {
		_ = os.Remove(e.ephemeralClusterCt)
		e.ephemeralClusterCt = ""
	}
	if e.ephemeralKindCfg != "" {
		_ = os.Remove(e.ephemeralKindCfg)
		e.ephemeralKindCfg = ""
	}
	if e.ephemeralCAPI != "" {
		_ = os.Remove(e.ephemeralCAPI)
		e.ephemeralCAPI = ""
	}
}
