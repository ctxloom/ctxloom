package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"golang.org/x/sync/singleflight"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// Coordinator hosting: every session-owning process — `ctxloom run`,
// `ctxloom acp`, and the bare `ctxloom mcp` fallback — stands the runtime
// coordinator up as a LIBRARY. Since the B1.6 surface shrink the gRPC
// channels are the ONLY agent ingress (tool surfaces live at each runner's
// local socket); this process keeps the host-relay handlers, each bound to
// the CALLER's credential-derived identity — never the host process's env
// (review R12f).

// NewHostedCoordinator builds and serves the coordinator for projectDir.
func NewHostedCoordinator(cfg *config.Config, projectDir string) (*coord.Coordinator, error) {
	key := ""
	if pid, _, err := taskops.ResolveProjectIdentity(projectDir); err == nil {
		key = pid
	} // best-effort: "" falls back to a path-derived key inside coord.New
	c, err := coord.New(coord.Options{
		Cfg:        cfg,
		ProjectDir: projectDir,
		ProjectKey: key,
		// A configurable RESOURCE ceiling (concurrent live engine
		// processes), not a correctness gate — see coord.agentConcurrencyCap's
		// doc. <= 0 (unset project config) falls back to the built-in
		// default inside coord.New.
		ConcurrencyCap: cfg.GetDelegationConcurrency(),
		// A configurable STRUCTURAL ceiling on the delegation tree's depth —
		// see coord.agentDepthCap's doc. <= 0 (unset project config) falls
		// back to the built-in default inside coord.New.
		Depth: cfg.GetDelegationDepth(),
		// The mailbox's shadow tee onto the file spool — off unless the
		// project asks for it. See config.DelegationConfig.SpoolTee.
		SpoolTee: cfg.GetDelegationSpoolTee(),
		// The spool CUTOVER — off unless the project asks for it. See
		// config.DelegationConfig.SpoolDelivery.
		SpoolDelivery: cfg.GetDelegationSpoolDelivery(),
	})
	if err != nil {
		return nil, err
	}
	// Host-relay tool handlers (B1.6 three-way routing): host-resident
	// tools relayed by runners as CustomRequest{ctxloom/<tool>} terminate
	// in THIS process, on a per-caller-identity ctxServer.
	c.SetCustomHandlers(coordCustomHandlers(cfg, c))
	if err := c.Serve(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// coordCustomHandlers builds the coordinator-side handlers for the
// host-resident tools (cross-session history, distillation — host session
// dirs are not mounted into children). Each call runs on a ctxServer bound
// to the CALLER's credential-derived identity, exactly like the stdio
// handlers — never the host process's env.
func coordCustomHandlers(cfg *config.Config, c *coord.Coordinator) map[string]coord.CustomHandler {
	// One dedupe group ACROSS the per-call ctxServers: a distillation already
	// in flight for a session is joined, not duplicated. Owned here because
	// serverFor mints a fresh ctxServer per relayed call — a group hung off
	// that would dedupe nothing.
	distill := &singleflight.Group{}
	serverFor := func(id coord.Identity) *ctxServer {
		return &ctxServer{cfg: cfg, self: id, agents: &agentDelegation{self: id, c: c}, distill: distill}
	}
	return map[string]coord.CustomHandler{
		coord.CustomToolPrefix + "compact_session": relayHost(serverFor, func(ctx context.Context, s *ctxServer, in compactSessionInput) (any, error) {
			_, out, err := s.handleCompactSession(ctx, nil, in)
			return out, err
		}),
		coord.CustomToolPrefix + "load_session": relayHost(serverFor, func(ctx context.Context, s *ctxServer, in loadSessionInput) (any, error) {
			_, out, err := s.handleLoadSession(ctx, nil, in)
			return out, err
		}),
		coord.CustomToolPrefix + "recover_session": relayHost(serverFor, func(ctx context.Context, s *ctxServer, in recoverSessionInput) (any, error) {
			_, out, err := s.handleRecoverSession(ctx, nil, in)
			return out, err
		}),
		coord.CustomToolPrefix + "get_previous_session": relayHost(serverFor, func(ctx context.Context, s *ctxServer, in getPreviousSessionInput) (any, error) {
			_, out, err := s.handleGetPreviousSession(ctx, nil, in)
			return out, err
		}),
		coord.CustomToolPrefix + "list_sessions": relayHost(serverFor, func(ctx context.Context, s *ctxServer, in listSessionsInput) (any, error) {
			_, out, err := s.handleListSessions(ctx, nil, in)
			return out, err
		}),
		coord.CustomToolPrefix + "evaluate_triggers": relayHost(serverFor, func(ctx context.Context, s *ctxServer, in evaluateTriggersInput) (any, error) {
			_, out, err := s.handleEvaluateTriggers(ctx, nil, in)
			return out, err
		}),
	}
}

// relayHost adapts one typed stdio handler onto the coordinator's relay
// seam: decode the relayed args into the SAME input struct, run the handler
// under the caller's identity, re-encode the result.
func relayHost[In any](serverFor func(coord.Identity) *ctxServer, h func(context.Context, *ctxServer, In) (any, error)) coord.CustomHandler {
	return func(ctx context.Context, caller coord.Identity, args json.RawMessage) (json.RawMessage, error) {
		var in In
		if len(args) > 0 {
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("decode arguments: %w", err)
			}
		}
		out, err := h(ctx, serverFor(caller), in)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("encode result: %w", err)
		}
		return raw, nil
	}
}

// HostCoordinatorForSession is the run/acp hosting helper: coordinator up,
// viewer socket bound under the owner harp, owner credential minted, and the
// engine-env pair (EnvCoordURL, EnvCoordCred) returned for injection at launch. A standup failure returns
// the error for the caller's fail-loud gate; the caller decides degraded
// behavior.
func HostCoordinatorForSession(cfg *config.Config, projectDir, ownerHarp, runtimeAxis string) (*coord.Coordinator, map[string]string, error) {
	c, err := NewHostedCoordinator(cfg, projectDir)
	if err != nil {
		return nil, nil, err
	}
	env, err := SessionOwnerEnv(c, ownerHarp, runtimeAxis)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return c, env, nil
}

// SessionOwnerEnv mints one session-owner credential on an already-hosted
// coordinator and returns the env pair for that owner's harness. D2 retired
// the per-owner-harp agent-bus.sock bind step: observe/roster/inject now
// ride ConsumerService, a single coordinator-wide surface Serve() already
// stood up — nothing left to bind here.
func SessionOwnerEnv(c *coord.Coordinator, ownerHarp, runtimeAxis string) (map[string]string, error) {
	token, err := c.RegisterSessionOwner(ownerHarp)
	if err != nil {
		return nil, err
	}
	url, err := c.ReachURL(runtimeAxis)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		coord.EnvCoordURL:  url,
		coord.EnvCoordCred: token,
	}, nil
}
