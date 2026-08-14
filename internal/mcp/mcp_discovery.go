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

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
//
// Harp and Stamp are graceful-egomaniac's marker-keying fix: the host
// runtime tier keys a marker's FILE NAME by cwd alone (workspaceKey), and a
// workspace:none delegated child shares its parent's cwd by construction, so
// two live runners can legitimately derive the identical marker name — the
// second's write silently orphans the first by discovery (iox.WriteFileAtomic
// gives no error, no warning). Carrying the OWNER's identity and build stamp
// INSIDE the marker lets probeWellKnownRunner tell "this marker is not mine"
// apart from "this marker's runner died", without needing the key itself to
// be collision-free — a marker that names a foreign harp is skipped and
// reaped rather than trusted, the same self-healing treatment a dead pid
// already gets. Stamp additionally seeds runMCPForward's post-connect
// verification with a hint (the connection's own handshake is the
// authoritative check — see mcp_forward.go's verifyForwardTarget).
type runnerDiscoveryMarker struct {
	Socket string `json:"socket"`
	Pid    int    `json:"pid"`
	Harp   string `json:"harp,omitempty"`
	Stamp  string `json:"stamp,omitempty"`
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
// Four outcomes, and this function is exactly what tells them apart:
//
//   - socket != "", err == nil: a marker names a runner that is ALIVE,
//     claims an identity this session has no objection to, and is REACHABLE
//     right now — forward to it (the fix: the child reaches the real runner
//     instead of a rogue local one). markerPath names which candidate
//     resolved, for the caller's mandatory pre-forward diagnostic
//     (graceful-egomaniac unit 1 — see mcp_server.go's ServeStdio).
//   - socket == "", err == nil: no USABLE marker anywhere for this cwd —
//     either nothing ever claimed this workspace as a runner-backed cell
//     (a LEGITIMATE standalone session, e.g. `ctxloom mcp serve` wired
//     directly into an editor with no runner/coordinator infrastructure at
//     all), or every candidate found was stale/foreign and got reaped (see
//     below). Local mode is correct here; the caller proceeds to normal
//     startup.
//   - err != nil: a marker names a runner whose PID IS STILL ALIVE and
//     whose claimed identity this session has no objection to, but its
//     socket refuses the connection — this cell WAS given a runner and it
//     is unreachable for some reason that is not "it exited cleanly"
//     (permissions, a wedged listener, something actively wrong). FAIL
//     LOUD: this is precisely the bug this file exists to close a silent
//     path around — never silently fall back to a local coordinator here.
//
// A marker that is DEAD (pid confirmed gone) or FOREIGN (claims a harp that
// disagrees with this session's own CTXLOOM_SESSION_HARP, when both are
// known — graceful-egomaniac's marker-keying fix, unit 3) is a fourth,
// unlabeled case: neither "the runner I should have" nor "broken", so it
// self-heals (the stale/foreign marker is removed) and probing falls through
// to the next candidate, ultimately to the socket=="",err==nil standalone
// verdict if no candidate pans out — never a false fail-loud over a runner
// that is simply gone, and never a silent hijack by a runner that is simply
// someone else's.
func probeWellKnownRunner(cwd string) (socket string, markerPath string, err error) {
	callerHarp := os.Getenv(agent.SessionHarpEnv)
	for _, path := range discoveryCandidates(cwd) {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			continue // this candidate was never published; try the next
		}
		var m runnerDiscoveryMarker
		if jerr := json.Unmarshal(raw, &m); jerr != nil || m.Socket == "" {
			continue // corrupt/empty marker — treat as absent, try the next
		}
		// Cheap liveness pre-check, no network dial: a confirmed-dead owner
		// pid never needs a socket probe to prove it is gone (habitable-cape
		// unit 4 — reap at discovery time too, not just at runner startup).
		if m.Pid > 0 && !pidalive.Probe(m.Pid).MaybeAlive() {
			_ = os.Remove(path)
			continue
		}
		// Identity pre-check, also no network dial: a marker naming a
		// DIFFERENT session than this one is not "ours" no matter how alive
		// or reachable its runner is — trusting it is exactly the measured
		// hijack (adopting a foreign runner's identity via the cwd-keyed
		// marker collision). Only compared when BOTH sides have an opinion:
		// an unset CTXLOOM_SESSION_HARP (a genuinely standalone shim, or a
		// harness that dropped it) has nothing to compare, so this axis is
		// silently skipped rather than guessed. The connect-time check in
		// runMCPForward (mcp_forward.go's verifyForwardTarget) is the
		// second, authoritative layer that also covers the build stamp.
		if callerHarp != "" && m.Harp != "" && callerHarp != m.Harp {
			clidiag.Warn("ctxloom", "mcp discovery: marker %s names runner harp %q, this session is %q — treating as a foreign marker (not ours) rather than risking a forward hijack; reaping it", path, m.Harp, callerHarp)
			_ = os.Remove(path)
			continue
		}
		if dialable(m.Socket) {
			return m.Socket, path, nil
		}
		// MaybeAlive (not a bare Alive check) treats an
		// unconfirmable probe the same as a live runner — falling through to
		// os.Remove + starting a fresh local coordinator on a false "dead"
		// would risk exactly the rogue-second-coordinator scenario the error
		// below warns about; erroring out on a genuinely unsure probe is the
		// safe direction.
		if m.Pid > 0 && pidalive.Probe(m.Pid).MaybeAlive() {
			return "", "", fmt.Errorf(
				"ctxloom mcp: discovery marker %s names a live runner (pid %d, socket %s) that refused the connection — refusing to silently start a local coordinator here, since that would be a SECOND, rogue coordinator whose agent_send(to:\"parent\") could never reach the real parent; check the runner process and its socket permissions, or set %s explicitly",
				path, m.Pid, m.Socket, coord.EnvMCPSocket,
			)
		}
		_ = os.Remove(path) // stale: the runner that wrote this is gone
	}
	return "", "", nil
}

// markerGlob returns every discovery-marker path currently present in dir —
// the host tier's cell-<key>.json files plus the container tier's fixed
// current.json, exactly the two shapes discoveryMarkerName ever produces.
// Deliberately narrow: dir also holds runnerSocketPath's own mcp-<pid>.sock
// listeners, which a reaper must never touch.
func markerGlob(dir string) []string {
	hostMarkers, _ := filepath.Glob(filepath.Join(dir, "cell-*.json"))
	out := append([]string{}, hostMarkers...)
	containerMarker := filepath.Join(dir, "current.json")
	if _, err := os.Stat(containerMarker); err == nil {
		out = append(out, containerMarker)
	}
	return out
}

// reapStaleDiscoveryMarkers removes every marker in dir whose recorded owner
// pid is CONFIRMED dead, and reports how many it removed. habitable-cape
// measured 12,334 such markers accumulated in one runtime dir, never reaped
// by anything — this is the sweep that closes that backlog, called once at
// every runner's startup (mcp_runner.go's ServeRunnerMCP) so the very first
// post-fix runner launch clears the existing debris (the one-shot cleanup
// effect), and every launch after that keeps the dir from reaccumulating it.
//
// Only Dead (not merely !MaybeAlive's caller-side "maybe alive" question,
// which some callers use to justify caution) is enough to CONFIRM absence;
// but the removal decision itself uses MaybeAlive's conservative direction —
// Alive or Unsure both skip — because reaping is destructive and a wrong
// "dead" verdict would delete a live runner's only discovery route out from
// under it. Corrupt/unreadable markers are left alone (best-effort;
// something else can diagnose a genuinely broken file) rather than swept as
// a side effect of a reader that cannot even parse them.
func reapStaleDiscoveryMarkers(dir string) int {
	removed := 0
	for _, path := range markerGlob(dir) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m runnerDiscoveryMarker
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if m.Pid > 0 && pidalive.Probe(m.Pid).MaybeAlive() {
			continue // still alive, or unsure — never reap on uncertainty
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}
