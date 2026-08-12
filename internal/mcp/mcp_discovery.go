package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/pidalive"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// HOST-CONTROLLED MCP DISCOVERY (fix/host-controlled-mcp-discovery)
//
// The env var CTXLOOM_MCP_SOCKET rides a VENDOR-CONTROLLED channel — a
// harness's ACP adapter is handed an mcpServers entry with a `env` array,
// and it is the ADAPTER, not ctxloom, that decides which of name/command/
// args/env actually reach the spawned `ctxloom mcp` process. codex-acp
// drops env on the floor (honors name/command/args only — verified against
// the running adapter). When that happens the shim used to find no socket
// and silently stand up its OWN in-process coordinator: a rogue second
// coordinator whose agent_send(to:"parent") can never reach the real
// parent three layers away — ctxloom's characteristic silent no-op.
//
// This file adds a discovery path no vendor adapter can drop: the runner
// publishes a small marker FILE (not a second socket) at a WELL-KNOWN
// filesystem location keyed by the cell's own workspace path, and the shim
// (which always runs IN that same cwd) probes for it unconditionally —
// no cooperation from the harness or adapter required. The env var stays
// the fast path; this is additive discovery, never a replacement of the
// wire (CTXLOOM_MCP_SOCKET / the unix-socket transport are unchanged).

// runnerDiscoveryMarker is what the runner publishes and the shim reads.
// Pid is what lets probeWellKnownRunner tell "unreachable because the
// runner died" (self-heal: stale, fall through to local) apart from
// "unreachable while the runner is still alive" (fail loud: something is
// actively broken, e.g. a permissions change, and local would be silently
// wrong).
type runnerDiscoveryMarker struct {
	Socket string `json:"socket"`
	Pid    int    `json:"pid"`
}

// discoveryMarkerName returns the well-known marker filename for kind at
// cwd, or ok=false if this tier publishes no marker at all
// (socketKindPrivateTemp — see runnerSocketPath). The writer (ServeRunnerMCP)
// and the reader (probeWellKnownRunner) both call this, so they can never
// drift apart on the name.
func discoveryMarkerName(kind socketKind, cwd string) (name string, ok bool) {
	switch kind {
	case socketKindContainer:
		// Exactly one runner per container (the agent-image convention) —
		// a fixed name is unambiguous and needs no key negotiation.
		return "current.json", true
	case socketKindHostRuntime:
		// Multiple runners can share $XDG_RUNTIME_DIR/ctxloom on one host
		// (no container isolation), so the marker is keyed by the cell's
		// own workspace path — the same cwd the shim it fronts also runs
		// in, so both sides derive the identical name independently.
		return "cell-" + workspaceKey(cwd) + ".json", true
	default:
		return "", false
	}
}

// workspaceKey derives a filesystem-safe, collision-resistant key from a
// cell's absolute working directory.
func workspaceKey(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:20]
}

// hostRuntimeSocketDir is $XDG_RUNTIME_DIR/ctxloom — the SAME directory
// runnerSocketPath's host tier binds sockets in ("" if XDG_RUNTIME_DIR is
// unset, matching that function's candidate() no-op on an empty dir).
func hostRuntimeSocketDir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "ctxloom")
}

// writeDiscoveryMarker publishes marker at dir/kind's well-known name,
// atomically (write-temp then rename, so a concurrent reader never sees a
// partial file). ok=false (no error) for tiers that publish no marker.
// Best-effort by design: the caller (ServeRunnerMCP) degrades to env-only
// discovery on failure rather than blocking the runner over it.
func writeDiscoveryMarker(dir string, kind socketKind, cwd string, marker runnerDiscoveryMarker) (cleanup func(), err error) {
	name, ok := discoveryMarkerName(kind, cwd)
	if !ok {
		return func() {}, nil
	}
	path := filepath.Join(dir, name)
	raw, err := json.Marshal(marker)
	if err != nil {
		return func() {}, fmt.Errorf("encode discovery marker: %w", err)
	}
	// Route through iox: a pid-suffixed temp name is unique across processes
	// but NOT across two runners in one process, and without an fsync a power
	// loss can persist the rename ahead of the marker's bytes — a shim would
	// then read a zero-length marker as a malformed one. iox.WriteFileAtomic
	// owns both.
	if err := iox.WriteFileAtomic(path, raw, 0o600); err != nil {
		return func() {}, fmt.Errorf("publish discovery marker %s: %w", path, err)
	}
	return func() { _ = os.Remove(path) }, nil
}

// discoveryCandidates returns the well-known marker paths a shim probes for
// cwd, in the SAME preference order runnerSocketPath tries when a runner
// picks its socket dir: container-local first, host-runtime second. The
// third runner tier (private MkdirTemp) publishes nothing — by
// construction nobody else could know that random path, so it is not a
// candidate here; the env var is the only route to a runner on that tier.
func discoveryCandidates(cwd string) []string {
	var out []string
	if name, ok := discoveryMarkerName(socketKindContainer, cwd); ok {
		out = append(out, filepath.Join(inContainerSocketDir, name))
	}
	if dir := hostRuntimeSocketDir(); dir != "" {
		if name, ok := discoveryMarkerName(socketKindHostRuntime, cwd); ok {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
}

// dialProbeTimeout bounds probeWellKnownRunner's liveness check — this runs
// on every shim startup with no marker configured by a human to tune, so it
// must fail fast rather than risk hanging a session launch on a wedged
// socket.
const dialProbeTimeout = 2 * time.Second

// dialable reports whether a unix socket at path currently accepts a
// connection.
func dialable(path string) bool {
	conn, err := net.DialTimeout("unix", path, dialProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// probeWellKnownRunner is the shim's SECOND discovery tier (after the
// CTXLOOM_MCP_SOCKET env var fast path): look for a runner discovery marker
// at cwd's well-known locations, with NO env var required — this is what
// survives a vendor ACP adapter dropping mcpServers.env (codex-acp).
//
// Three outcomes, and this function is exactly what tells them apart:
//
//   - socket != "", err == nil: a marker names a runner that is ALIVE and
//     REACHABLE right now — forward to it (the fix: the child reaches the
//     real runner instead of a rogue local one).
//   - socket == "", err == nil: no marker anywhere for this cwd — nothing
//     ever claimed this workspace as a runner-backed cell, so this is a
//     LEGITIMATE standalone session (e.g. `ctxloom mcp serve` wired
//     directly into an editor, no runner/coordinator infrastructure at
//     all). Local mode is correct here; the caller proceeds to normal
//     startup.
//   - err != nil: a marker names a runner whose PID IS STILL ALIVE but its
//     socket refuses the connection — this cell WAS given a runner and it
//     is unreachable for some reason that is not "it exited cleanly"
//     (permissions, a wedged listener, something actively wrong). FAIL
//     LOUD: this is precisely the bug this file exists to close a silent
//     path around — never silently fall back to a local coordinator here.
//
// A marker whose pid is DEAD is a fourth, unlabeled case: a stale leftover
// from a runner that exited without running its close() cleanup (docker
// stop / kill -9 skip deferred cleanup). That is indistinguishable from "no
// runner was ever here" for THIS session's purposes, so it self-heals
// (remove the stale marker) and falls through to the next candidate, and
// ultimately to the socket=="",err==nil standalone verdict if no candidate
// pans out — never a false fail-loud over a runner that is simply gone.
func probeWellKnownRunner(cwd string) (socket string, err error) {
	for _, path := range discoveryCandidates(cwd) {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			continue // this candidate was never published; try the next
		}
		var m runnerDiscoveryMarker
		if jerr := json.Unmarshal(raw, &m); jerr != nil || m.Socket == "" {
			continue // corrupt/empty marker — treat as absent, try the next
		}
		if dialable(m.Socket) {
			return m.Socket, nil
		}
		// MaybeAlive (not a bare Alive check) treats an
		// unconfirmable probe the same as a live runner — falling through to
		// os.Remove + starting a fresh local coordinator on a false "dead"
		// would risk exactly the rogue-second-coordinator scenario the error
		// below warns about; erroring out on a genuinely unsure probe is the
		// safe direction.
		if m.Pid > 0 && pidalive.Probe(m.Pid).MaybeAlive() {
			return "", fmt.Errorf(
				"ctxloom mcp: discovery marker %s names a live runner (pid %d, socket %s) that refused the connection — refusing to silently start a local coordinator here, since that would be a SECOND, rogue coordinator whose agent_send(to:\"parent\") could never reach the real parent; check the runner process and its socket permissions, or set %s explicitly",
				path, m.Pid, m.Socket, coord.EnvMCPSocket,
			)
		}
		_ = os.Remove(path) // stale: the runner that wrote this is gone
	}
	return "", nil
}
