package cli

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// Coordinator hosting: every session-owning process — `ctxloom run`,
// `ctxloom acp`, and the bare `ctxloom mcp` fallback — stands the runtime
// coordinator up as a LIBRARY and serves ctxloom's whole MCP toolset over its
// authenticated streamable-HTTP endpoint. Identity is per credential, so the
// shared server never sees the host process's env as caller identity
// (review R12f): each credential gets its own ctxServer instance.

// newHostedCoordinator builds and serves the coordinator for projectDir.
func newHostedCoordinator(cfg *config.Config, projectDir string) (*coord.Coordinator, error) {
	key := ""
	if pid, _, err := taskops.ResolveProjectIdentity(projectDir); err == nil {
		key = pid
	} // best-effort: "" falls back to a path-derived key inside coord.New
	c, err := coord.New(coord.Options{
		Cfg:        cfg,
		ProjectDir: projectDir,
		ProjectKey: key,
	})
	if err != nil {
		return nil, err
	}
	if err := c.Serve(newCoordServerFactory(cfg, c)); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// newCoordServerFactory builds the per-identity MCP tool surface the
// coordinator's HTTP endpoint serves: one full ctxServer (context, session,
// memory, agent tools + resources) per credential, with the CALLER's
// identity baked in.
func newCoordServerFactory(cfg *config.Config, c *coord.Coordinator) coord.ServerFactory {
	return func(id coord.Identity) *mcp.Server {
		return newCtxServerForIdentity(cfg, c, id)
	}
}

// newCtxServerForIdentity assembles one identity-bound MCP server over the
// shared coordinator.
func newCtxServerForIdentity(cfg *config.Config, c *coord.Coordinator, id coord.Identity) *mcp.Server {
	s := &ctxServer{
		cfg:    cfg,
		self:   id,
		agents: &agentDelegation{self: id, c: c},
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Version: Version,
	}, &mcp.ServerOptions{Instructions: sessionInstructions(id.Harp)})
	s.registerTools(server)
	s.registerResources(server)
	return server
}

// hostCoordinatorForSession is the run/acp hosting helper: coordinator up,
// viewer socket bound under the owner harp, owner credential minted, and the
// engine-env trio returned for injection at launch. A standup failure returns
// the error for the caller's fail-loud gate; the caller decides degraded
// behavior.
func hostCoordinatorForSession(cfg *config.Config, projectDir, ownerHarp, runtimeAxis string) (*coord.Coordinator, map[string]string, error) {
	c, err := newHostedCoordinator(cfg, projectDir)
	if err != nil {
		return nil, nil, err
	}
	env, err := sessionOwnerEnv(c, ownerHarp, runtimeAxis)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return c, env, nil
}

// sessionOwnerEnv mints one session-owner credential on an already-hosted
// coordinator and returns the env trio for that owner's harness.
func sessionOwnerEnv(c *coord.Coordinator, ownerHarp, runtimeAxis string) (map[string]string, error) {
	if err := c.BindSessionSocket(ownerHarp); err != nil {
		clidiag.Warn("ctxloom", "agent bus (viewer verbs) unavailable: %v", err)
	}
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
