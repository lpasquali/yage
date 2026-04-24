// bootstrap-capi is the Go port of bootstrap-capi.sh.
//
// Goal: identical CLI surface (same flags, same env vars, same exit codes,
// same log output format). Internal structure is modular Go packages under
// internal/, one per concern.
//
// The port is incremental. Today:
//   - CLI parse matches bash parse_options() 1:1
//   - Usage output matches bash usage() (embedded from the .sh header)
//   - Dependency installers (ensure_*) are ported
//   - Top-level orchestration is stubbed with pointers back to bash line ranges
package main

import (
	"os"

	"github.com/lpasquali/bootstrap-capi/internal/bootstrap"
	"github.com/lpasquali/bootstrap-capi/internal/cli"
	"github.com/lpasquali/bootstrap-capi/internal/config"
)

func main() {
	cfg := config.Load()
	cli.Parse(cfg, os.Args[1:])
	os.Exit(bootstrap.Run(cfg))
}
