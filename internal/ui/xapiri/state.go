package xapiri

// state.go — shared state for the eight-step xapiri walkthrough.
//
// Per docs/abstraction-plan.md §22, the walkthrough forks at step 0
// into "on-prem" and "cloud" branches that diverge materially at
// steps 4 and 5 (provider-pick vs budget; capacity vs cost-compare).
// Steps 1, 2, 3 share their shape across forks but ask slightly
// different questions; steps 6, 7, 8 are identical in code.
//
// We carry the user's answers across step methods on a single
// *state value rather than threading a dozen positional args. The
// fields fall in three buckets:
//
//   - cfg / w / r: the inputs threaded in from Run(). cfg is the
//     resolved config we mutate; w is the writer the prompts speak
//     on; r is the bufio-backed reader from prompts.go.
//   - fork / workload: walkthrough-local answers that aren't on
//     cfg yet (Track α may add cfg.Workload later — until then we
//     keep them here and stamp them into cfg fields the rest of
//     yage already reads, like ControlPlaneMachineCount).
//   - feasibilityVerdict: the result of the feasibility gate at
//     step 5, stashed so step 7 can re-display it without
//     re-running the check.

import (
	"errors"
	"io"
	"os"

	"github.com/lpasquali/yage/internal/config"
)

// forkType discriminates the on-prem vs cloud branches of the
// walkthrough. forkUnknown is the "ask explicitly" outcome from
// auto-detection — it never sticks past step 0.
type forkType int

const (
	forkUnknown forkType = iota
	forkOnPrem
	forkCloud
)

func (f forkType) String() string {
	switch f {
	case forkOnPrem:
		return "on-prem"
	case forkCloud:
		return "cloud"
	}
	return "unknown"
}

// envTier captures step 1's dev/staging/prod choice. Drives Argo /
// Hubble UI / monitoring add-ons and replica counts. Uses string
// values so the review step can echo them verbatim and so the
// persisted Secret is human-readable.
type envTier string

const (
	envDev     envTier = "dev"
	envStaging envTier = "staging"
	envProd    envTier = "prod"
)

// resilienceTier captures step 2's resilience choice. The set of
// valid values differs per fork (on-prem caps at HA-across-hosts;
// cloud goes up to HA-multi-region). Drives cp_nodes and (cloud-
// only) NAT GW / multi-AZ replica counts.
type resilienceTier string

const (
	resilienceSingle  resilienceTier = "single"     // single-host on-prem / single-AZ cloud
	resilienceHA      resilienceTier = "ha"         // HA across hosts (on-prem) / HA single-region (cloud)
	resilienceHAMulti resilienceTier = "ha-multi"   // cloud-only: HA across regions
)

// appBucket is one (count × template) pair from step 3. The user
// can mix multiple buckets ("6 medium + 2 heavy") so we store a
// slice on workloadShape.
type appBucket struct {
	Count    int
	Template string // "light" | "medium" | "heavy"
}

// workloadShape collects step 3's answers. Held local to xapiri
// rather than on cfg because Track α (§23 feasibility gate) hasn't
// landed cfg.Workload yet; once it does, this struct's fields move
// over to cfg.Workload and the feasibility shim wires up.
type workloadShape struct {
	Apps        []appBucket
	DBGB        int
	EgressGBMo  int  // 0 on on-prem (no prompt); required on cloud
	HasQueue    bool
	HasObjStore bool
	HasCache    bool
}

// state is the running walkthrough's mutable bag. Each step method
// (step1_environment, step2_resilience, …) reads the relevant cfg
// + state fields, prompts, and writes back. Step methods return
// error; ErrUserExit is the sentinel for "user pressed ^D / chose
// no at the final confirm" and triggers a clean exit-0.
type state struct {
	w   io.Writer
	cfg *config.Config
	r   *reader

	fork     forkType
	env      envTier
	resil    resilienceTier
	workload workloadShape

	// Cloud-fork-only: the budget the user typed at step 4 plus
	// the headroom percent. budgetAfterHeadroom = budget × (1 - hp).
	budgetUSDMonth      float64
	headroomPct         float64
	budgetAfterHeadroom float64

	// feasibilityErr, when non-nil, captures the most recent
	// feasibility-gate verdict the walkthrough can't satisfy. The
	// step-5 loop-back path consults this. Reset on each step-5
	// retry.
	feasibilityErr error

	// deployNow is set by step 8's final yes/no prompt. main can
	// read it via Run's return code (we still return 0, but the
	// caller has cfg in hand and the orchestrator picks up from
	// there on next run regardless).
	deployNow bool
}

// newState constructs a state with the bufio-backed reader the
// existing prompts.go helpers expect. We read from os.Stdin and
// write to the io.Writer Run() was given. Tests can drive the
// state machine by injecting both.
func newState(w io.Writer, cfg *config.Config) *state {
	return &state{
		w:           w,
		cfg:         cfg,
		r:           newReader(os.Stdin, w),
		headroomPct: 0.20,
	}
}

// newStateWithReader is the seam tests would use to drive the
// state machine via a *bufio.Scanner-backed reader on a
// non-os.Stdin source. Currently unused; kept here so a future
// test pass can plug in without touching the interactive path.
func newStateWithReader(w io.Writer, cfg *config.Config, in io.Reader) *state {
	return &state{
		w:           w,
		cfg:         cfg,
		r:           newReader(in, w),
		headroomPct: 0.20,
	}
}

// ErrUserExit is the sentinel a step method returns when the user
// has chosen to bail out cleanly (chose "no" at the final confirm,
// hit EOF on stdin, etc.). Run() translates it to exit code 0 —
// the spirits rest, no harm done.
var ErrUserExit = errors.New("xapiri: user exit")
