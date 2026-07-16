package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// mcpSocketEnvVar duplicates agentcoord/coord.EnvMCPSocket's value
// ("CTXLOOM_MCP_SOCKET") as a literal rather than importing it: that package
// pulls in internal/operations -> internal/lm/backends -> this package
// (backends registers acp.NewACP()), so importing it here would cycle. See
// internal/agentcoord/coord/identity.go's EnvMCPSocket doc for the canonical
// source of truth on this variable's meaning and lifecycle.
const mcpSocketEnvVar = "CTXLOOM_MCP_SOCKET"

// containerProfileBackend maps ACPConfig.AgentEngine's kiro/claude/codex/agy
// vocabulary (see its doc comment above) onto isolation's container-profile
// backend-registry keys ("claude-code" / "kiro" / "codex" / "opencode" /
// "antigravity" — see internal/lm/isolation/profile.go's containerProfileFor
// doc): the two vocabularies were never unified. Names already shared
// between them (kiro, codex, opencode) pass through unchanged; an
// unrecognized/empty engine name ALSO passes through unchanged —
// containerProfileFor treats an unrecognized key as its default
// (claude-oriented) profile, the same fallback an unconfigured agent_engine
// gets elsewhere in this driver (chatArgv's --agent-engine is likewise
// only-appended-when-set).
func containerProfileBackend(agentEngine string) string {
	switch strings.ToLower(agentEngine) {
	case claudeEngineName:
		return "claude-code"
	case "agy":
		return "antigravity"
	default:
		return agentEngine
	}
}

// containerTransport runs `<agent> acp` INSIDE the engine's container image
// instead of on the host — the runtime axis's honoring half (ISO1). It
// reuses the SAME isolation-cell machinery `ctxloom run --agent` uses for a
// containerized engine (runtime probe, image ensure/build, auth resolution,
// the shared-fs probe, the identical-path project mount, session-state
// mounts — see isolation.NewContainerFor / Container.PrepareWorkspace). The
// only genuinely NEW piece is the adapter onto a plain-stdio JSON-RPC
// subprocess (isolation.RunAttached) instead of SpawnClient's
// go-plugin-over-socket transport, which serves an entirely different
// protocol (`ctxloom llm serve`) that this driver never speaks.
//
// FAIL LOUD, never a silent host fallback: unlike `run --agent`'s degrade
// chain (chainFor/Prepare), which CAN fall back to the host under
// --degraded, an editor session that asked for container isolation and
// silently got the host instead would be lying to the user about being
// isolated — the exact incoherence the old warn-and-ignore existed to avoid,
// now replaced by actually honoring the axis. So PrepareWorkspace's error
// (or any error below it) is always returned AS the session-open failure —
// there is deliberately no --degraded escape hatch on this path.
func (b *ACP) containerTransport(ctx context.Context, argv []string, env map[string]string, workDir string) (*transport, error) {
	engine := b.agentEngine
	if engine == "" {
		engine = b.command
	}

	rt := isolation.SelectRuntime("")
	if !rt.Available() {
		return nil, fmt.Errorf("acp: agent %q needs runtime:container but no container runtime is reachable (docker or podman CLI on PATH with its daemon up) — install/start docker or podman, or switch this agent's runtime to host", engine)
	}

	pol := isolation.NewContainerFor(rt, containerProfileBackend(b.agentEngine))
	state := isolation.SessionStateFromEnv(env)
	pol = pol.WithSessionState(state)

	agentID := state.Harp
	if agentID == "" {
		agentID = "acp-" + engine
	}

	ws, err := pol.PrepareWorkspace(ctx, workDir, agentID)
	if err != nil {
		return nil, fmt.Errorf("acp: container isolation for %q could not start (install/build the agent image and start the container runtime, or switch this agent's runtime to host): %w", engine, err)
	}

	command := append([]string{b.BinaryPath}, argv...)
	extraEnv, extraMounts := containerReachBackEnv(rt)
	// The chat's own model/session env (spawnEnv's overlay, already curated —
	// never the full host os.Environ(): the container gets a fresh, minimal
	// env by design, unlike spawnHostTransport's os.Environ() passthrough).
	for k, v := range env {
		extraEnv = append(extraEnv, k+"="+v)
	}

	spec, err := pol.ExecSpec(ws, command, extraEnv, extraMounts)
	if err != nil {
		_ = ws.Cleanup()
		return nil, fmt.Errorf("acp: container isolation for %q: %w", engine, err)
	}

	ac, err := isolation.RunAttached(ctx, rt, spec)
	if err != nil {
		_ = ws.Cleanup()
		return nil, fmt.Errorf("acp: starting %q in container: %w", engine, err)
	}

	return &transport{
		stdin:  ac.Stdin,
		stdout: ac.Stdout,
		close: func() error {
			cerr := ac.Close()
			if werr := ws.Cleanup(); werr != nil && cerr == nil {
				cerr = werr
			}
			return cerr
		},
	}, nil
}

// containerReachBackEnv builds the reach-back env/mount pair that lets an
// in-container MCP server the engine spawns (one of session/new's
// mcpServers — a `ctxloom mcp` stdio shim) still reach the HOST runner's MCP
// endpoint. It SHARES the runner-terminated-MCP mechanism the container
// delegation track already built for exactly this cross-boundary problem
// (agentcoord's coord.EnvMCPSocket / internal/cli/mcp_forward.go — a
// container-local-or-host unix socket the shim dials over HTTP), rather than
// forking a new reach-back path.
//
// The runner (`ctxloom llm serve <backend>`, this process's own parent
// invocation — see internal/cli/llm_serve.go) stands the socket up on its
// OWN filesystem and exports the path into its own env BEFORE the engine
// spawns. That is directly reachable when the engine also runs on the host
// (the common case today), but the engine now runs in a DIFFERENT mount
// namespace, so the socket's directory is bind-mounted in at the SAME path —
// identical-path, exactly like the workspace mount (invariant 1) — through
// the runtime's own path mapper (ExposeIdentical), and the env var is
// forwarded UNCHANGED: the value is still a valid path once the directory it
// names is mounted at that same path inside the container.
//
// No socket → no mount/env: nothing to share, and an in-container engine
// simply cannot reach the runner's MCP surface — no worse than a bare host
// self-invoke with no runner dial-home behind it, which already degrades the
// same way (mcp_server.go's forward mode is a no-op without the var set).
func containerReachBackEnv(rt isolation.Runtime) ([]string, []isolation.Mount) {
	sock := os.Getenv(mcpSocketEnvVar)
	if sock == "" {
		return nil, nil
	}
	dir := filepath.Dir(sock)
	return []string{mcpSocketEnvVar + "=" + sock}, []isolation.Mount{rt.ExposeIdentical(dir, false)}
}
