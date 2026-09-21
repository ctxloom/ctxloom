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

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
)

// Execute is the RAW launch — the only tail. Refuse a foreign engine → bind
// the session to the engine (Instance: requiredness is the engine's
// refusal; a Structured launch needs a driver) → Redeem → Decode →
// Configure → Serve (the session's endpoint, under the launch's identity)
// → Deliver (Static.Deliver over the Launch's Loadout, under the session's
// writer) → Drive. There is no Execute that skips delivery and no way to
// hold a Launch that Resolve did not make.
func Execute(ctx context.Context, deps Deps, l launch.Launch) (Outcome, error) {
	if deps.Driver == nil {
		return Outcome{}, ErrNoDriver
	}
	if deps.Kind == nil {
		return Outcome{}, ErrNoKind
	}
	if deps.Static == nil {
		return Outcome{}, ErrNoStatic
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
	lo := l.Loadout(pkg)
	var closeServed func()
	if deps.Dynamic != nil {
		served, err := deps.Dynamic.Serve(ctx, lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
		if err != nil {
			return Outcome{}, fmt.Errorf("runner: serve the session's endpoint: %w", err)
		}
		closeServed = func() { _ = served.Close() }
	}
	delivered, err := deps.Static.Deliver(ctx, lo, deps.Kind.Root().Surfaces(), l.Target(deps.Records))
	if err != nil {
		if closeServed != nil {
			closeServed()
		}
		return Outcome{}, fmt.Errorf("runner: deliver the launch: %w", err)
	}
	env := l.EngineEnv()
	servers := bindEndpoint(agent.ComposeChatMCPServers(pkg.MCP, nil), l.MCP)
	mcpConfig := mcpFileOf(delivered)
	turn := Turn{
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
	return Outcome{Delivered: delivered, MCPConfig: mcpConfig, Close: closeServed}, nil
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

// bindEndpoint points ctxloom's own server entry at the session's bound
// endpoint: the URL the runner serves on and the bearer every request must
// carry, in place of the stdio command the package declares. The engine then
// dials the runner directly; no shim process is spawned. A set with no
// ctxloom entry (the builtin server withheld) is returned unchanged — that
// child has no reach-back, which its spawn already warned about.
func bindEndpoint(servers []agent.ChatMCPServer, ep sessions.Endpoint) []agent.ChatMCPServer {
	if ep.URL == "" {
		return servers
	}
	for i, srv := range servers {
		if srv.Name != agent.MCPServerName {
			continue
		}
		servers[i] = agent.ChatMCPServer{
			Name:      agent.MCPServerName,
			Transport: agent.MCPTransportHTTP,
			URL:       ep.URL,
			Headers:   map[string]string{"Authorization": "Bearer " + ep.Credential},
		}
	}
	return servers
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
