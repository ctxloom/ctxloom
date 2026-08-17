package acp

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/mcpsocket"
)

// mcpSocketEnvVar duplicates agentcoord/coord.EnvMCPSocket's value
// ("CTXLOOM_MCP_SOCKET") as a literal rather than importing it: that package
// pulls in internal/operations -> internal/lm/backends -> this package
// (backends registers acp.NewACP()), so importing it here would cycle. See
// internal/agentcoord/coord/identity.go's EnvMCPSocket doc for the canonical
// source of truth on this variable's meaning and lifecycle. The copy is BOUND
// to that original by constants_binding_test.go, an external test package that
// is not in the cycle — a rename on either side fails there rather than
// silently leaving a containerized engine with no MCP surface.
const mcpSocketEnvVar = "CTXLOOM_MCP_SOCKET"

// isolationBackendFor maps ACPConfig.AgentEngine's kiro/claude/codex
// vocabulary (see its doc comment above) onto isolation's container-spec
// backend-registry keys ("claude-code" / "kiro" / "codex" / "opencode" —
// see internal/lm/isolation/enginespec.go's engineContainerSpecFor doc): the two
// vocabularies were never unified. Names already shared between them (kiro,
// codex, opencode) pass through unchanged; an unrecognized/empty engine name
// ALSO passes through unchanged — and engineContainerSpecFor's default arm
// FAILS CLOSED on auth (noContainerAuth), so an unmapped name does not get a
// working generic spec: PrepareWorkspace aborts loudly, and agent-binding
// validation refuses runtime:container for engines with no container auth
// before a run ever launches. That is deliberate — the default used to hand
// the user's Anthropic credentials to whatever engine happened to be
// unrecognized. "agy" is one such unrecognized name post-0.7.0: antigravity's
// container spec was removed with the engine, so a containerized "agy" fails
// loudly rather than routing to a spec that no longer exists. The generic
// "acp" backend is in the same position: registered and selectable, but with
// no container auth, so it cannot run containerized until a spec is added in
// engineContainerSpecFor.
func isolationBackendFor(agentEngine string) string {
	switch strings.ToLower(agentEngine) {
	case claudeEngineName:
		return "claude-code"
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
func (b *ACP) containerTransport(ctx context.Context, argv []string, env map[string]string, workDir, runtimeAxis string) (*transport, error) {
	engine := b.agentEngine
	if engine == "" {
		engine = b.command
	}

	// The DEMANDED ownership rides into selection. Rootless and rootful
	// containers differ in UID mapping, so a runtime in the other mode is not
	// a substitute for the one this session asked for — and satisfying the
	// request with it is exactly the lie the fail-loud contract above exists
	// to prevent, just one level deeper than a host fallback.
	rt := isolation.SelectRuntime("", isolation.RuntimeAxis(runtimeAxis))
	// SelectRuntime never reports "unavailable" via a sentinel value: on no
	// launchable docker/podman IN THE DEMANDED OWNERSHIP it falls back to
	// Host{}, whose Available() is UNCONDITIONALLY true (Host can always run
	// a bare subprocess — the fault-tolerant floor other isolation policies
	// rely on). A type check against Host is the only way to tell "no usable
	// container runtime" apart from "found one" — the exact test
	// isolation.go's own chainFor uses for the identical decision (see its
	// rt.(Host) check).
	if _, isHost := rt.(isolation.Host); isHost {
		return nil, fmt.Errorf("acp: agent %q needs runtime:%s but no container runtime with that ownership is reachable (docker or podman CLI on PATH with its daemon up) — install/start one, or switch this agent's runtime to host", engine, runtimeAxis)
	}

	pol := isolation.NewContainerFor(rt, isolationBackendFor(b.agentEngine))
	if b.containerImage != "" {
		pol = pol.WithImage(b.containerImage)
	}
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
	extraEnv, extraMounts, reachBackClose, err := containerReachBackEnv(rt, runtime.GOOS)
	if err != nil {
		_ = ws.Cleanup()
		return nil, fmt.Errorf("acp: reach-back for %q: %w", engine, err)
	}
	spec, err := pol.ExecSpec(ws, command, extraEnv, extraMounts)
	if err != nil {
		if reachBackClose != nil {
			_ = reachBackClose()
		}
		_ = ws.Cleanup()
		return nil, fmt.Errorf("acp: container isolation for %q: %w", engine, err)
	}

	// The chat's own model/session env (envOverlay's product, already curated —
	// never the full host os.Environ(): the container gets a fresh, minimal env
	// by design, unlike spawnHostTransport's os.Environ() passthrough) rides
	// RunAttached's value-carrying channel, so each key crosses as a bare-name
	// `-e NAME` and its VALUE stays off the world-readable `run` argv. This map
	// is caller-supplied and its contents are not ours to vet, so it never goes
	// through spec.Env's KEY=VAL form.
	ac, err := isolation.RunAttached(ctx, rt, spec, env)
	if err != nil {
		if reachBackClose != nil {
			_ = reachBackClose()
		}
		_ = ws.Cleanup()
		return nil, fmt.Errorf("acp: starting %q in container: %w", engine, err)
	}

	// The in-container engine needs the SAME post-stdin-EOF grace the host
	// transport gives it (see shutdownGrace / DefaultShutdownGrace): the adapter
	// flushes its native transcript asynchronously, and here the destructive act
	// is a container removal that takes the filesystem holding that half-written
	// transcript with it. Zero (every production path) leaves isolation's own
	// default; the test seam's shorter value carries through.
	ac.ShutdownGrace = b.shutdownGrace

	return &transport{
		stdin:  ac.Stdin,
		stdout: ac.Stdout,
		// The container's streamed stderr tail — the in-container engine's
		// dying words, captured BEFORE ac.Close force-removes the container
		// (after which `docker logs` has nothing left to read).
		stderrTail: ac.StderrTail,
		close: func() error {
			cerr := ac.Close()
			if reachBackClose != nil {
				if berr := reachBackClose(); berr != nil && cerr == nil {
					cerr = berr
				}
			}
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
// (agentcoord's coord.EnvMCPSocket / internal/mcp/mcp_forward.go — a
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
//
// OFF-LINUX FALLBACK (this reach-back was broken there until now — verified):
// the mount above is the identical-path-bind-mount crossing
// isolation.NewContainerFor's every OTHER mount already relies on, which
// only works because Linux shares ONE kernel between host and container. On
// macOS/Windows Docker Desktop the container runs inside a Linux VM, so a
// unix socket FILE bind-mounted in from the host is not a live endpoint
// there — the identical rationale the go-plugin container transport once
// carried before its off-Linux TCP path was deleted in 0.7 (that transport
// is now Linux-only unix-socket-over-bind-mount; internal/lm/isolation).
// That deleted fallback solved the OPPOSITE direction, though: there the
// plugin SERVER ran in-container and the HOST dialled OUT to a docker-
// published loopback port. Here the runner's MCP endpoint (the unix listener
// behind sock) is the SERVER and runs on the HOST, and the in-container
// `ctxloom mcp` shim is the one that must dial OUT — no `-p` publish helps
// that direction (publish forwards host->container, never the reverse).
// Instead this bridges the unix socket onto a HOST-loopback TCP port (no
// container boundary crossed making that bridge: it dials sock from THIS
// same process, which the runner's own `ctxloom llm serve` invocation
// created), and the container reaches it via Docker Desktop's automatic
// host.docker.internal DNS name (Podman's equivalent is
// host.containers.internal — reachBackDialHost picks per rt.Name()), which
// both Docker Desktop and Podman Desktop route to the host's own network
// stack, loopback included, with no `-p`/`--add-host` needed on macOS/
// Windows. goos is threaded explicitly (never read via runtime.GOOS inline)
// so this selection is unit-testable for every host OS from any CI machine.
func containerReachBackEnv(rt isolation.Runtime, goos string) ([]string, []isolation.Mount, func() error, error) {
	sock := os.Getenv(mcpSocketEnvVar)
	if sock == "" {
		// Degraded, never fatal — but never silent either: this function is only
		// reached under container isolation, where the missing endpoint means the
		// in-container engine cannot reach ctxloom's MCP surface AT ALL, so every
		// ctxloom tool the session's loadout advertised is absent while the
		// session otherwise looks healthy. A runner normally exports this before
		// the engine spawns (internal/cli/llm_runner_common.go), so its absence
		// says the engine was launched outside one, or that the runner's own MCP
		// endpoint failed to stand up.
		warnf("acp: %s is unset, so this containerized engine gets no ctxloom MCP surface (no ctxloom tools in-session) — expected when the engine was not launched by a ctxloom runner", mcpSocketEnvVar)
		return nil, nil, nil, nil
	}
	if goos == "linux" {
		dir := filepath.Dir(sock)
		return []string{mcpSocketEnvVar + "=" + sock}, []isolation.Mount{rt.ExposeIdentical(dir, false)}, nil, nil
	}
	bridge, err := startReachBackBridge(sock)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("acp: reach-back TCP bridge for %s: %w", mcpSocketEnvVar, err)
	}
	addr := fmt.Sprintf("%s%s:%d", mcpsocket.TCPPrefix, reachBackDialHost(rt), bridge.port)
	return []string{mcpSocketEnvVar + "=" + addr}, nil, bridge.Close, nil
}

// reachBackDialHost is the DNS name Docker/Podman Desktop resolve, from
// INSIDE a container, to the host's own network stack (loopback included) —
// the reverse-direction equivalent of a `-p` publish, and the reason the
// bridge below only needs to bind 127.0.0.1, never 0.0.0.0. Docker and
// Podman ship different names for it; rt.Name() is already threaded through
// every call site here, so branching on it costs nothing new.
func reachBackDialHost(rt isolation.Runtime) string {
	if rt.Name() == "podman" {
		return "host.containers.internal"
	}
	return "host.docker.internal"
}

// reachBackBridge is the host-loopback TCP<->unix proxy that stands in for
// the bind-mounted socket file off Linux. It never crosses a container
// boundary itself: both its listener and its dial of sock run in THIS
// process, on the same host filesystem the runner's own unix listener (sock)
// is already bound to — only the TCP side is ever reachable from inside the
// container.
type reachBackBridge struct {
	port int
	ln   net.Listener
}

// startReachBackBridge reserves a free 127.0.0.1 port and starts proxying
// every accepted connection to a fresh dial of sock, one goroutine pair per
// connection, until Close stops the listener (in-flight proxied connections
// drain on their own; nothing here waits for them, matching ac.Close's own
// fire-and-forget teardown of the attached container).
func startReachBackBridge(sock string) (*reachBackBridge, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve reach-back bridge port: %w", err)
	}
	b := &reachBackBridge{port: ln.Addr().(*net.TCPAddr).Port, ln: ln}
	go b.acceptLoop(sock)
	return b, nil
}

// acceptLoop runs until the listener closes (Close, or an unexpected accept
// error) — the normal go-plugin-style "serve until closed" shape, matching
// ServeRunnerMCP's own `go func() { _ = srv.Serve(ln) }()`.
func (b *reachBackBridge) acceptLoop(sock string) {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.pipe(conn, sock)
	}
}

// pipe dials sock fresh for this one connection and splices the two
// directions until either side closes — a plain byte-for-byte proxy; the
// HTTP-over-unix traffic riding it (mcp_forward.go's StreamableClientTransport)
// never needs to know it crossed a bridge.
func (b *reachBackBridge) pipe(conn net.Conn, sock string) {
	defer func() { _ = conn.Close() }()
	uc, err := net.Dial("unix", sock)
	if err != nil {
		// The shim on the container side only ever sees a connection reset, and
		// the cause is entirely on this side of the boundary — so this is the one
		// place it can be reported at all.
		warnf("acp: reach-back bridge could not dial the runner MCP socket %q, so the in-container shim's connection was reset: %v", sock, err)
		return
	}
	defer func() { _ = uc.Close() }()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(uc, conn); close(done) }()
	_, _ = io.Copy(conn, uc)
	<-done
}

// Close stops accepting new bridge connections (idempotent: net.Listener's
// own Close is).
func (b *reachBackBridge) Close() error { return b.ln.Close() }
