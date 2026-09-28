// Package runner (adapter) is the process that receives ONE Launch, redeems
// and decodes its package, delivers it into the cell, and drives the engine.
// It runs on whatever host it is on — the human's machine or the inside of a
// container — and re-derives nothing: every value it needs rode the Launch.
//
// Execute is the ONE tail every launch ends in — a delegated child's and an
// owner run's alike — so a host `run --agent X` and an `agent_run X` deliver
// the same file set by construction: both build the Launch's Loadout and
// hand it to the ONE static writer (delivery.Static) under the session's
// writer tag. The session's MCP endpoint is BOUND here, at the address the
// Launch carries (runner/mcp is the Dynamic port), and the engine's MCP
// file names it as URL + bearer through the same delivery. The drive is
// EngineHost's, reached through the Driver port.
package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
)

// Deps are the runner's ports, composed once per process.
type Deps struct {
	// Kind is the ONE engine this runner hosts — what its RunnerHello
	// advertised. A launch naming another is refused before delivery; the
	// launch's session is bound to it (Instance) before delivery, so a
	// session the engine cannot run is refused by the engine, by name.
	Kind engine.Engine
	// Inline and ClaimCheck are the same two transports the originator
	// carried with; the carrier's shape names which redeems.
	Inline     composite.Transport
	ClaimCheck composite.Transport
	// Static is the ONE static writer: it delivers the plan's items through
	// the hosted engine's typed approaches under the session's writer tag.
	Static delivery.Static
	// Records is the ownership record the delivery writes under.
	Records delivery.Ownership
	// Dynamic BINDS the session's MCP endpoint — the one the Launch carries
	// — before the engine is driven, and returns its closer. nil serves
	// nothing.
	Dynamic delivery.Dynamic
	// Reporter receives the diagnostics delivering the launch raises; the
	// runner's composition chooses the sink. Nil discards.
	Reporter report.Sink
	// Configure applies the label's own body to the hosted engine before
	// anything is delivered or driven; nil when the engine takes no
	// configuration.
	Configure func(body map[string]any) error
	// Driver drives the engine once the launch is delivered.
	Driver Driver
	// Unsetenv removes a variable from this process's environment, which
	// every engine spawn starts from: how a launch's Cell.Unset is honoured.
	// Injected like MainDeps.Unsetenv, so the refusal is testable; nil
	// refuses any launch that names something to unset.
	Unsetenv func(string) error
}

// Driver is the engine-drive port: EngineHost implements it.
type Driver interface {
	Drive(ctx context.Context, t Turn) error
}

// Outcome is what Execute reports once the engine is driven.
type Outcome struct {
	// Delivered is what the static writer reported.
	Delivered delivery.Delivered
	// Close tears the served surface down (nil when none was served).
	Close func()
	// MCPConfig is the engine's MCP file as delivered ("" when the plan
	// carries no MCP item).
	MCPConfig string
}

var (
	// ErrWrongEngine refuses a launch naming an engine this runner does not
	// host.
	ErrWrongEngine = errors.New("runner: the launch names an engine this runner does not host")
	// ErrNoDriver refuses to execute with nothing to drive the engine.
	ErrNoDriver = errors.New("runner: no driver is composed")
	// ErrNoKind refuses to execute with no engine composed.
	ErrNoKind = errors.New("runner: no engine kind is composed")
	// ErrNoStatic refuses to execute with no static writer composed.
	ErrNoStatic = errors.New("runner: no static writer is composed")
	// ErrEngineEnvUnscrubbed refuses to drive an engine that would inherit a
	// variable its launch says it must not (Cell.Unset).
	ErrEngineEnvUnscrubbed = errors.New("runner: a variable the engine must not inherit could not be removed")
)

// Execute is the RAW launch — the only tail. Refuse a foreign engine → bind
// the session to the engine (Instance: requiredness is the engine's
// refusal; a Structured launch needs a driver) → Redeem → Decode →
// Configure → Serve (the session's endpoint, under the launch's identity)
// → Deliver (Static.Deliver over the Launch's Loadout, under the session's
// writer) → Drive. There is no Execute that skips delivery and no way to
// hold a Launch that Resolve did not make.
func Execute(ctx context.Context, deps Deps, l launch.Launch) (Outcome, error) {
	l, inst, err := prepareLaunch(deps, l)
	if err != nil {
		return Outcome{}, err
	}
	if err := scrubEngineEnv(deps, l.Cell.Unset); err != nil {
		return Outcome{}, err
	}
	pkg, err := composite.Open(ctx, deps.Inline, deps.ClaimCheck, l.Package)
	if err != nil {
		return Outcome{}, err
	}
	if err := configureLabel(deps, l); err != nil {
		return Outcome{}, err
	}
	lo := l.Loadout(pkg)
	// The ONE rendering of the package for this engine: what the static
	// writer delivers and what the structured drive names, ctxloom's own
	// session-endpoint entry rendered through the engine's dynamic approach
	// from the endpoint the launch carries (delivery.InputsFor).
	inputs, err := delivery.InputsFor(lo, deps.Kind.Root().Dynamic)
	if err != nil {
		return Outcome{}, err
	}
	closeServed, err := serveEndpoint(ctx, deps, lo)
	if err != nil {
		return Outcome{}, err
	}
	delivered, err := deliverAndDrive(ctx, deps, l, inst, pkg, lo, inputs)
	if err != nil {
		if closeServed != nil {
			closeServed()
		}
		return Outcome{}, err
	}
	return Outcome{Delivered: delivered, MCPConfig: mcpFileOf(delivered), Close: closeServed}, nil
}

// prepareLaunch refuses missing deps and a foreign engine, opens a container
// cell's roots at their Engine side, and binds the session to the engine
// (whose Instance refusal passes through; a Structured launch needs a
// driver).
func prepareLaunch(deps Deps, l launch.Launch) (launch.Launch, engine.Instance, error) {
	if deps.Driver == nil {
		return l, nil, ErrNoDriver
	}
	if deps.Kind == nil {
		return l, nil, ErrNoKind
	}
	if deps.Static == nil {
		return l, nil, ErrNoStatic
	}
	// The runner shares the engine's filesystem wherever it runs (a
	// container's foreground process, or beside the engine on the host), so
	// every root is opened at its Engine side — on the host that IS the Host
	// side, so one read is right under every runtime and nothing here asks
	// which one it is under. Rewritten once, before anything reads the cell:
	// the engine session's roots, the static target, the drive's paths.
	l.Cell.Paths = l.Cell.Paths.EngineSide()
	hosted := deps.Kind.Root().Name
	if l.Engine != hosted {
		return l, nil, fmt.Errorf("%w: hosts %q, launch names %q", ErrWrongEngine, hosted, l.Engine)
	}
	inst, err := deps.Kind.Instance(l.Session())
	if err != nil {
		return l, nil, err
	}
	if l.Mode == engine.Structured && len(inst.Drivers()) == 0 {
		return l, nil, engine.ErrUnsupported{Engine: hosted, Capability: "drive"}
	}
	return l, inst, nil
}

// configureLabel hands the label's body to the composed Configure, when both
// exist.
func configureLabel(deps Deps, l launch.Launch) error {
	if deps.Configure == nil || l.Label.Body == nil {
		return nil
	}
	if err := deps.Configure(l.Label.Body); err != nil {
		return fmt.Errorf("runner: configure %s from label %q: %w", l.Engine, l.Label.Label, err)
	}
	return nil
}

// serveEndpoint serves the session's endpoint when a dynamic server is
// composed, returning its closer (nil when nothing was served).
func serveEndpoint(ctx context.Context, deps Deps, lo delivery.Loadout) (func(), error) {
	if deps.Dynamic == nil {
		return nil, nil
	}
	served, err := deps.Dynamic.Serve(ctx, lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	if err != nil {
		return nil, fmt.Errorf("runner: serve the session's endpoint: %w", err)
	}
	return func() { _ = served.Close() }, nil
}

// deliverAndDrive delivers the loadout under the session's writer, composes
// the engine's exec over what was presented, and drives the first turn. The
// caller tears the served endpoint down on error.
func deliverAndDrive(ctx context.Context, deps Deps, l launch.Launch, inst engine.Instance, pkg composite.Package, lo delivery.Loadout, inputs delivery.Inputs) (delivery.Delivered, error) {
	delivered, err := deps.Static.Deliver(ctx, lo, deps.Kind.Root(), l.Target(deps.Records))
	if err != nil {
		return delivery.Delivered{}, fmt.Errorf("runner: deliver the launch: %w", err)
	}
	ex, err := inst.Exec(delivered.Presented)
	if err != nil {
		return delivery.Delivered{}, fmt.Errorf("runner: compose the engine's exec: %w", err)
	}
	// The exec's env holds ONLY the engine-native variables; the launch's
	// engine env — the identity carriers, the label's env, the caller's
	// passthrough — is laid under it here, the engine's own on top.
	env := l.EngineEnv()
	maps.Copy(env, ex.Env)
	ex.Env = env
	turn := Turn{
		Launch:     l,
		Instance:   inst,
		Exec:       ex,
		MCPServers: agent.ComposeChatMCPServers(inputs.MCP.Servers, nil),
		Prompt:     firstTurn(pkg, l),
		Presented:  delivered.Presented,
	}
	if err := deps.Driver.Drive(ctx, turn); err != nil {
		return delivery.Delivered{}, err
	}
	return delivered, nil
}

// mcpFileOf is the host path the MCP kind's presentation names, "" when the
// plan delivered none: what the drive's chat request points the engine at.
func mcpFileOf(d delivery.Delivered) string {
	for i, k := range d.Wrote {
		if k == present.MCP && i < len(d.Presented) {
			return d.Presented[i].HostPath
		}
	}
	return ""
}

// firstTurn is the first turn's lead: the composed context ahead of the
// prompt on a fresh spawn; the prompt alone when the engine resumes its own
// recorded session by native key.
func firstTurn(pkg composite.Package, l launch.Launch) string {
	if l.Resume.NativeKey != "" {
		return l.Prompt
	}
	return textblocks.Join(pkg.Context.Text, l.Prompt)
}

// scrubEngineEnv removes every variable the launch says the engine must not
// inherit from this process's environment before anything is spawned: every
// engine spawn starts from it (os.Environ), and this process hosts exactly
// one run. The launch's own env is laid over it afterwards, so a variable the
// launch SETS still reaches the engine.
func scrubEngineEnv(deps Deps, unset []string) error {
	if len(unset) == 0 {
		return nil
	}
	if deps.Unsetenv == nil {
		return fmt.Errorf("%w: no environment to remove %s from is composed", ErrEngineEnvUnscrubbed, strings.Join(unset, ", "))
	}
	var errs []error
	for _, k := range unset {
		if err := deps.Unsetenv(k); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrEngineEnvUnscrubbed, errors.Join(errs...))
	}
	return nil
}
