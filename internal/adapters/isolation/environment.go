package isolation

import (
	"context"
	"errors"
	"os/exec"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Environment is where one run executes. It is constructed from host facts
// and the engine's declared needs (Spec); it owns relocation and every
// mapping and mount rule internally, and exposes only the outcome. The host
// and the container environment implement it identically; nothing on it
// names a mount, a runtime or a side.
//
// Every environment shares the host's filesystem — the host in place, the
// container by bind mount — so the Placement's two sides of a root are two
// views of the SAME files. Remote systems with no shared filesystem are out
// of scope by ruling; the launch verbs, Listen and the Placement's Env would
// generalise to one, the file contract would not.
type Environment interface {
	// Placement is the outcome the engine is handed: every root on both
	// sides, and the env that makes each declared need true where it runs.
	Placement() launch.Placement
	// Listen is what the coordinator must listen on so this environment's
	// runner can dial home (zero where the runner reaches host loopback).
	Listen() present.Listen
	// Start launches the runner in this environment. It returns once the
	// runner is up where it runs; readiness to serve is the coordinator's
	// awaitRunner.
	Start(ctx context.Context, r RunnerRequest) (*RunnerHandle, error)
	// Interactive is an interactive launch's runner as a command the
	// originator starts on its pty, with the teardown of whatever it started
	// beyond the process.
	Interactive(ctx context.Context, r RunnerRequest) (Interactive, error)
	// Describe is for DISPLAY only (the preview and the banner); no caller
	// branches on it.
	Describe() Description
	// Cleanup releases the environment. Safe to call once, after the run.
	Cleanup() error
}

// RunnerRequest is one runner to start: the engine it hosts, the label it
// runs under, how chatty it is, and the RUNNER's env (the reach-back trio),
// never the engine's.
type RunnerRequest struct {
	Engine, Label string
	Verbosity     int
	Env           map[string]string
}

// Interactive is an interactive runner: the command the originator starts on
// the pty it holds, the name its roster entry shows ("" where it is a plain
// process), and the teardown of whatever the environment started beyond that
// process (nil when there is none). Teardown's ctx ends its wait for a
// runner that has not yet appeared: cancel it once the command has exited.
type Interactive struct {
	Cmd      *exec.Cmd
	Name     string
	Teardown func(context.Context) error
}

// Description names an environment for display: its workspace axis, the
// runtime it runs under and how its runner reaches home.
type Description struct{ Workspace, Runtime, Reach string }

// RuntimeUnavailable and ReachUnknown are what a preview describes where the
// run would refuse: no runtime with the requested ownership is reachable, or
// the probe for a route home failed. Naming them, rather than the host the
// degrade chain falls back to or the loopback a route-less probe defaults
// to, is what keeps a preview from showing an outcome no run would get.
const (
	RuntimeUnavailable = "unavailable"
	ReachUnknown       = "unknown"
)

// ErrPreviewEnvironment is a launch asked of a preview: a preview relocates
// and probes, and never starts anything.
var ErrPreviewEnvironment = errors.New("isolation: a preview environment cannot start a runner")

// Prepare walks the degrade chain for s's axes and returns the prepared
// environment. Findings — a dropped container boundary, an unpreparable
// session home — are recorded as strictness findings for the caller's gate;
// an error is returned for what no degrade can fix: a root the chosen
// environment cannot present (present.ErrUnreachableRoot).
//
// Two stages, in order. Stage 1 builds the LOCAL layout at host paths on
// every environment: the workspace (live project or checkout) and the
// session home (launch.SessionHome, created and prepared here). Stage 2 is
// the chosen environment's relocator: the host presents every root in place,
// the container presents each one together with the mount that makes it
// true. It is the only place a path is rewritten.
func Prepare(ctx context.Context, s Spec) (Environment, error) {
	chain := withSessionState(chainFor(s.axes, s.backend(), s.img), s.state)
	p, ws := prepareChain(ctx, chain, s.axes.Runtime, s.project, s.harp)
	r := p.relocator()
	l := stageLayout(s, ws.Dir(), workspaceEnv(ws), r.sharesLogin())
	pl, roots, err := r.relocate(l)
	if err != nil {
		refuseUnpresentable(err)
		_ = ws.Cleanup()
		return nil, err
	}
	env, err := p.environment(ws, pl, roots)
	if err != nil {
		_ = ws.Cleanup()
		return nil, err
	}
	return env, nil
}

// unreachableRootRemedy names the fix for a root the runtime cannot route.
const unreachableRootRemedy = "move the project and the ctxloom home onto a filesystem the container runtime can mount (the daemon must see the same paths), or run with `runtime: host`"

// refuseUnpresentable records the refusal of an environment that cannot
// present every root: the finding Prepare refuses on, and the one Preview
// reports where the run would.
func refuseUnpresentable(err error) {
	strictness.FailAlways(report.KindIsolation, unreachableRootRemedy, "refusing to run in an environment that cannot present every root: %v", err)
}

// Preview is Prepare's relocation with no effects on disk: no checkout, no
// scratch, no session home created. A container preview PROBES the runtime
// read-only — its selection (info) and its route home (network inspect) — so
// Describe and Listen show the real runtime and reach. Start and Interactive
// return ErrPreviewEnvironment; Cleanup is a no-op.
//
// A preview does NOT stop at the first problem. Every refusal a run would hit
// is recorded as the finding the run raises — through the same call, not a
// copy — for the caller's gate, and the preview carries on with the outcome it
// can still compute: a root no runtime routes is marked unreachable (a Host
// side and no Engine side, never a guessed path), an unreachable runtime is
// described as RuntimeUnavailable and an unprobed route as ReachUnknown.
func Preview(ctx context.Context, s Spec) Environment {
	p := chainFor(s.axes, s.backend(), s.img)[0]
	if s.axes.WantsContainer() && !IsContainerPolicyName(p.Name()) {
		// chainFor recorded the refusal. p is the host fallback the run
		// refuses to take, so only its workspace axis is shown: no runtime
		// routes any root.
		_, desc := p.preview(ctx)
		desc.Runtime, desc.Reach = RuntimeUnavailable, ReachUnknown
		return previewEnvironment{placement: unrouted(previewLayout(s, false)), desc: desc}
	}
	r := p.relocator()
	pl, _, err := r.relocate(previewLayout(s, r.sharesLogin()))
	if err != nil {
		refuseUnpresentable(err)
	}
	listen, desc := p.preview(ctx)
	return previewEnvironment{placement: pl, listen: listen, desc: desc}
}

// previewEnvironment is Preview's result: the outcome, and nothing to start.
type previewEnvironment struct {
	placement launch.Placement
	listen    present.Listen
	desc      Description
}

func (e previewEnvironment) Placement() launch.Placement { return e.placement }
func (e previewEnvironment) Listen() present.Listen      { return e.listen }
func (e previewEnvironment) Describe() Description       { return e.desc }
func (previewEnvironment) Cleanup() error                { return nil }

func (previewEnvironment) Start(context.Context, RunnerRequest) (*RunnerHandle, error) {
	return nil, ErrPreviewEnvironment
}

func (previewEnvironment) Interactive(context.Context, RunnerRequest) (Interactive, error) {
	return Interactive{}, ErrPreviewEnvironment
}
