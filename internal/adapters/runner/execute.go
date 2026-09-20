// Package runner (adapter) is the process that receives ONE Launch, redeems
// and decodes its package, delivers it into the cell, and drives the engine.
// It runs on whatever host it is on — the human's machine or the inside of a
// container — and re-derives nothing: every value it needs rode the Launch.
//
// Execute is the ONE tail every launch ends in — a delegated child's and an
// owner run's alike — so a host `run --agent X` and an `agent_run X` deliver
// the same file set by construction: both projections feed the same writers
// with the same package. Until the static writers land (Part 4.1, slice 12)
// the writers are the engine's own Setup, and until the engine host moves
// beside this package (14a) the drive is coord.EngineHost's, reached through
// the Driver port.
package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"

	"github.com/ctxloom/ctxloom/internal/shared/report"
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
	// Static delivers the package's surfaces into the cell: today the hosted
	// engine's own Setup.
	Static Static
	// Dynamic BINDS the session's MCP endpoint — the one the Launch carries
	// — before the engine is driven, and returns its closer. nil serves
	// nothing.
	Dynamic delivery.Dynamic
	// Surfaces validates the binding's delivery preference (as written on
	// the package) against the hosted engine's declaration; nil accepts the
	// engine's default delivery.
	Surfaces agent.SurfaceResolver
	// Reporter receives the diagnostics delivering the launch raises; the
	// runner's composition chooses the sink. Nil discards.
	Reporter report.Sink
	// Configure applies the label's own body to the hosted engine before
	// anything is delivered or driven; nil when the engine takes no
	// configuration.
	Configure func(body map[string]any) error
	// Driver drives the engine once the launch is delivered.
	Driver Driver
}

// Static is the static-delivery port as today's writers expose it: the
// engine backend's Setup over the managed payload.
type Static interface {
	Setup(ctx context.Context, req *agent.SetupRequest) error
}

// Driver is the engine-drive port: coord.EngineHost implements it.
type Driver interface {
	Drive(ctx context.Context, t coord.Turn) error
}

// Outcome is what Execute reports once the engine is driven.
type Outcome struct {
	// Delivered is the package as delivered: what the writers were given.
	Delivered *agent.ManagedConfig
	// Close tears the served surface down (nil when none was served).
	Close func()
	// MCPConfig is the .mcp.json under the session home the engine's argv
	// names ("" when the package registers no server).
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
)

// mcpConfigName is the file the runner delivers the composed server set as,
// under the session home: the engine's argv names it, so a structured turn
// registers the same servers a delivered MCP surface does.
const mcpConfigName = ".mcp.json"

// Execute is the RAW launch — the only tail. Refuse a foreign engine → bind
// the session to the engine (Instance: requiredness is the engine's
// refusal; a Structured launch needs a driver) → Redeem → Decode →
// Configure → Serve (the session's endpoint, under the launch's identity)
// → Deliver (the writers, the session's .mcp.json) → Drive. There is no
// Execute that skips delivery and no way to hold a Launch that Resolve did
// not make.
func Execute(ctx context.Context, deps Deps, l launch.Launch) (Outcome, error) {
	if deps.Driver == nil {
		return Outcome{}, ErrNoDriver
	}
	if deps.Kind == nil {
		return Outcome{}, ErrNoKind
	}
	hosted := deps.Kind.Root().Name
	if l.Engine != hosted {
		return Outcome{}, fmt.Errorf("%w: hosts %q, launch names %q", ErrWrongEngine, hosted, l.Engine)
	}
	inst, err := deps.Kind.Instance(l.Session())
	if err != nil {
		return Outcome{}, err
	}
	if l.Mode == engine.Structured && len(inst.Drivers()) == 0 {
		return Outcome{}, engine.ErrUnsupported{Engine: hosted, Capability: "drive"}
	}
	pkg, err := composite.Open(ctx, deps.Inline, deps.ClaimCheck, l.Package)
	if err != nil {
		return Outcome{}, err
	}
	if deps.Configure != nil && l.Label.Body != nil {
		if err := deps.Configure(l.Label.Body); err != nil {
			return Outcome{}, fmt.Errorf("runner: configure %s from label %q: %w", l.Engine, l.Label.Label, err)
		}
	}
	var closeServed func()
	if deps.Dynamic != nil {
		lo := delivery.Loadout{Plan: l.Plan, Package: pkg, Exports: l.Exports, Index: l.Index, MCP: l.MCP, Identity: l.Identity, WorkDir: l.Cell.Workspace}
		served, err := deps.Dynamic.Serve(ctx, lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
		if err != nil {
			return Outcome{}, fmt.Errorf("runner: serve the session's endpoint: %w", err)
		}
		closeServed = func() { _ = served.Close() }
	}
	managed := agent.ManagedConfigFor(agent.ManagedSurfaces{Hooks: pkg.Hooks, MCP: pkg.MCP, DenyTools: pkg.DenyTools, Statusline: pkg.Statusline}, l.Exports)
	agent.PreferSurfaces(report.To(deps.Reporter), managed, string(l.Engine), pkg.Selection.Preference, deps.Surfaces)
	env := l.EngineEnv()
	if err := deps.Static.Setup(ctx, &agent.SetupRequest{
		Reporter:  deps.Reporter,
		WorkDir:   l.Cell.Workspace,
		Fragments: contextFragments(pkg),
		Env:       env,
		Managed:   managed,
		CellKind:  coordgrpc.CellKindOf(l.Cell),
		Form:      agent.LaunchFormDeliver,
		Model:     l.Label.Model,
	}); err != nil {
		return Outcome{}, fmt.Errorf("runner: deliver the launch: %w", err)
	}
	servers := agent.ComposeChatMCPServers(pkg.MCP, nil)
	mcpConfig := ""
	if len(servers) > 0 {
		mcpConfig = filepath.Join(sessionHome(l), mcpConfigName)
		if err := agent.WriteChatMCPConfigFile(mcpConfig, servers); err != nil {
			return Outcome{}, fmt.Errorf("runner: deliver %s: %w", mcpConfig, err)
		}
	}
	turn := coord.Turn{
		Launch: l,
		Chat: agent.ChatRequest{
			WorkDir:     l.Cell.Workspace,
			Model:       l.Label.Model,
			Env:         env,
			Permissions: l.Permission,
			// ForwardPermissions is false: ctxloom does not broker a second
			// approval UI on top of the engine's own. A delegated run has no
			// human upstream of it, so a prompt it does raise parks with
			// nobody to answer it — which is why the resolver floors a
			// Structured child to a headless-safe posture.
			ForwardPermissions: false,
			MCPServers:         servers,
			MCPConfigPath:      mcpConfig,
			ResumeSessionID:    l.Resume.NativeKey,
		},
		Prompt: firstTurn(pkg, l),
	}
	if err := deps.Driver.Drive(ctx, turn); err != nil {
		if closeServed != nil {
			closeServed()
		}
		return Outcome{}, err
	}
	return Outcome{Delivered: managed, MCPConfig: mcpConfig, Close: closeServed}, nil
}

// contextFragments is the assembled context as the writers take it: one
// lead fragment, none when the package composed no context.
func contextFragments(pkg composite.Package) []*agent.Fragment {
	if pkg.Context.Text == "" {
		return nil
	}
	return []*agent.Fragment{{Content: pkg.Context.Text}}
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

// sessionHome is where the session's own files land: the engine home the
// cell bound when the binding asked for one, else the session dir.
func sessionHome(l launch.Launch) string {
	paths := l.Cell.Paths.Paths()
	if paths.EngineHome.Host != "" {
		return paths.EngineHome.Host
	}
	return paths.Scratch.Host
}
