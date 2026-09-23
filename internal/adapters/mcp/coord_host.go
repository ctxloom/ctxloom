package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"golang.org/x/sync/singleflight"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// Coordinator hosting: the session-owning process — `ctxloom run` — stands
// the runtime coordinator up as a LIBRARY, and it is the ONLY process that
// does. hostCoordinator is private, reachable only through
// HostCoordinatorForSession, so no other entry point in this package can
// build one. The gRPC channels are the ONLY agent ingress (tool surfaces
// live at each runner's session endpoint); this process keeps the host-relay
// handlers, each bound to the CALLER's credential-derived identity — never
// the host process's env.

// CoordinatorConstructor is the composition root's way to construct the
// runtime coordinator (cli.NewCoordinator in production, coord.New in
// tests): coord.New is called only under cmd/*.
type CoordinatorConstructor func(coord.Options) (*coord.Coordinator, error)

// hostCoordinator assembles the hosted coordinator's Options from the App's
// config and asks the composition to construct it, then serves it.
// ownerHarp is the session owner's harp — the inbox this process drains
// (coord.Options.OwnerHarp); the hosting site knows it before standing the
// coordinator up.
func hostCoordinator(build CoordinatorConstructor, app *operations.App, projectDir, ownerHarp string) (*coord.Coordinator, error) {
	cfg, err := app.Config(context.Background())
	if err != nil {
		return nil, err
	}
	key := ""
	if pid, _, err := taskops.ResolveProjectIdentity(projectDir); err == nil {
		key = pid
	} // best-effort: "" falls back to a path-derived key inside coord.New
	host := NewHostApp(cfg)
	c, err := build(coord.Options{
		ProjectDir: projectDir,
		ProjectID:  key,
		// The host-relayed tools (Verbs.Host) terminate in THIS process, on a
		// per-caller-identity ctxServer.
		Host: host,
		// A configurable RESOURCE ceiling (concurrent live engine
		// processes), not a correctness gate — see coord.agentConcurrencyCap's
		// doc. <= 0 (unset project config) falls back to the built-in
		// default inside coord.New.
		ConcurrencyCap: cfg.GetDelegationConcurrency(),
		// A configurable STRUCTURAL ceiling on the delegation tree's depth —
		// see coord.agentDepthCap's doc. <= 0 (unset project config) falls
		// back to the built-in default inside coord.New.
		Depth: cfg.GetDelegationDepth(),
		// The idle reaper's bound — delegation.idle_timeout, resolved by config.
		IdleTimeout: cfg.GetDelegationIdleTimeout(),
		OwnerHarp:   ownerHarp,
	})
	if err != nil {
		return nil, err
	}
	host.Bind(c)
	if err := coordgrpc.Serve(c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// HostApp is coord.HostApp over the host-resident tools (cross-session
// history, distillation, triggers, context status — host session dirs are
// not mounted into children). Each call runs on a ctxServer bound to the
// CALLER's credential-derived identity, exactly like the stdio handlers —
// never the host process's env.
type HostApp struct {
	cfg *config.Config
	// distill is ONE dedupe group ACROSS the per-call ctxServers: a
	// distillation already in flight for a session is joined, not
	// duplicated. Owned here because Serve mints a fresh ctxServer per call —
	// a group hung off that would dedupe nothing.
	distill *singleflight.Group
	// c is the coordinator whose relay this is, bound once it exists
	// (Bind): the host an internal one-shot a relayed tool starts runs on.
	c     *coord.Coordinator
	tools map[string]hostTool
}

// hostTool serves one relayed tool: decode the args into the tool's own
// input struct, run its handler under the caller's server, encode the result.
type hostTool func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error)

// NewHostApp composes the relayed tool set over cfg.
func NewHostApp(cfg *config.Config) *HostApp {
	return &HostApp{cfg: cfg, distill: &singleflight.Group{}, tools: map[string]hostTool{
		"compact_session": hostTool(func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error) {
			return decodeThen(args, func(in compactSessionInput) (any, error) {
				_, out, err := s.handleCompactSession(ctx, nil, in)
				return out, err
			})
		}),
		"load_session": hostTool(func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error) {
			return decodeThen(args, func(in loadSessionInput) (any, error) {
				_, out, err := s.handleLoadSession(ctx, nil, in)
				return out, err
			})
		}),
		"recover_session": hostTool(func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error) {
			return decodeThen(args, func(in recoverSessionInput) (any, error) {
				_, out, err := s.handleRecoverSession(ctx, nil, in)
				return out, err
			})
		}),
		"get_previous_session": hostTool(func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error) {
			return decodeThen(args, func(in getPreviousSessionInput) (any, error) {
				_, out, err := s.handleGetPreviousSession(ctx, nil, in)
				return out, err
			})
		}),
		"list_sessions": hostTool(func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error) {
			return decodeThen(args, func(in listSessionsInput) (any, error) {
				_, out, err := s.handleListSessions(ctx, nil, in)
				return out, err
			})
		}),
		"evaluate_triggers": hostTool(func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error) {
			return decodeThen(args, func(in evaluateTriggersInput) (any, error) {
				_, out, err := s.handleEvaluateTriggers(ctx, nil, in)
				return out, err
			})
		}),
		"context_status": hostTool(func(ctx context.Context, s *ctxServer, args json.RawMessage) (any, error) {
			return decodeThen(args, func(in contextStatusInput) (any, error) {
				_, out, err := s.handleContextStatus(ctx, nil, in)
				return out, err
			})
		}),
	}}
}

// Serve implements coord.HostApp: the named tool under the caller's
// identity; a name outside the relayed set is refused by name.
func (a *HostApp) Serve(ctx context.Context, caller coord.Identity, req coord.HostRequest) (coord.HostResult, error) {
	tool, ok := a.tools[req.Tool]
	if !ok {
		return coord.HostResult{}, fmt.Errorf("%w: %q", coord.ErrUnknownHostTool, req.Tool)
	}
	s := &ctxServer{cfg: a.cfg, self: caller, distill: a.distill, hosts: a}
	out, err := tool(ctx, s, req.Args)
	if err != nil {
		return coord.HostResult{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return coord.HostResult{}, fmt.Errorf("%s: encode result: %w", req.Tool, err)
	}
	return coord.HostResult{Body: raw}, nil
}

// decodeThen decodes the relayed args into the tool's own input struct (an
// absent object is the zero input) and runs the handler.
func decodeThen[In any](args json.RawMessage, h func(In) (any, error)) (any, error) {
	var in In
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("decode arguments: %w", err)
		}
	}
	return h(in)
}

var _ coord.HostApp = (*HostApp)(nil)

// Bind names the coordinator this relay serves. Called once, by the host
// that built both; the coordinator is constructed after its HostApp, so it
// cannot be a constructor argument.
func (a *HostApp) Bind(c *coord.Coordinator) { a.c = c }

// RunHost implements operations.RunHosts: a relayed tool's one-shot runs on
// the session's own coordinator, whatever harp it was minted as.
func (a *HostApp) RunHost(context.Context, string, string) (operations.RunHost, error) {
	if a.c == nil {
		return nil, operations.ErrNoRunHost
	}
	return a.c, nil
}

// HostCoordinatorForSession is the run hosting helper: coordinator up, the
// owner registered under ownerHarp, and the owner's credential returned —
// the identity the owner-owned run is minted under (coord.Identify) and the
// credential the host revokes on teardown. A standup failure returns the
// error for the caller's fail-loud gate; the caller decides degraded
// behavior. The owner's RUNNER is stamped by StartOwnedRun with its own
// per-run trio; nothing here rides an environment.
func HostCoordinatorForSession(build CoordinatorConstructor, app *operations.App, projectDir, ownerHarp string) (*coord.Coordinator, string, error) {
	c, err := hostCoordinator(build, app, projectDir, ownerHarp)
	if err != nil {
		return nil, "", err
	}
	token, err := c.RegisterSessionOwner(ownerHarp)
	if err != nil {
		c.Close()
		return nil, "", err
	}
	return c, token, nil
}
