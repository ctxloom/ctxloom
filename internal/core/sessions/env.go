package sessions

import "errors"

// The process-boundary carriers. These constants are the ONLY spellings; the
// env-literals-once arch gate forbids the literals outside this package.
//
// Two carriers, two codecs, no overlap:
//   - the RUNNER process receives the reach-back trio (EncodeReach);
//   - the ENGINE process (and its hook subprocesses) receives the two
//     identity values hooks and taskloom key on (HookEnv).
const (
	// EnvCoordURL is the coordinator's MCP endpoint URL
	// (http://host:port/mcp); the gRPC RunnerChannel rides the same
	// host:port (one h2c listener, content-type routed).
	EnvCoordURL = "CTXLOOM_COORD_URL"
	// EnvCoordCred is the caller's bearer token (identity, not just
	// admission). 256-bit, hex; only its SHA-256 is ever persisted. The
	// credential is read from the HARNESS-INHERITED process env only — it is
	// never written into any MCP config structure, file, or Env map.
	EnvCoordCred = "CTXLOOM_COORD_CRED"
	// EnvRunID is the coordinator-minted run id for a spawned child's
	// runner, so RunnerChannel Hello correlates the runner to the run the
	// coordinator will StartRun on it. Absent on the parent-session
	// credential.
	EnvRunID = "CTXLOOM_RUN_ID"
	// EnvHarp carries the run's session harp to the ENGINE process and its
	// hook subprocesses; it names the session dir and the spool.
	EnvHarp = "CTXLOOM_SESSION_HARP"
	// EnvProjectID carries the project id the run serves, so a containerized
	// child's taskloom keys the SAME shared host log.
	EnvProjectID = "CTXLOOM_PROJECT_ID"

	// The carriers below are the per-spawn seam's remaining keys. Each is
	// replaced by a typed field on the launch (the MCP endpoint, the cell
	// workspace) or by the identity itself (depth, one-shot), and leaves
	// with the slice that carries that value.

	// EnvMCPSocket is the container-local (or host user-private) unix
	// socket path of the RUNNER's MCP endpoint. The runner creates the
	// socket BEFORE the harness spawns and exports this into the harness
	// env; a `ctxloom mcp` shim finding it forwards the whole surface there.
	//
	// This var has a SECOND value shape, which every reader must handle: a
	// container transport cannot bind-mount a live unix socket across the
	// Docker Desktop VM boundary off Linux, so there it instead carries
	// "tcp://host:port" — a host-loopback TCP bridge onto the same unix
	// socket (the marker is mcpsocket.TCPPrefix).
	EnvMCPSocket = "CTXLOOM_MCP_SOCKET"
	// EnvCellWorkDir carries the prepared workspace directory (a worktree's
	// per-agent checkout) to the plugin-hosted `llm serve` process at spawn
	// time, so its MCP discovery marker is keyed by the SAME directory the
	// shim's cwd derives from. The runner learns its cell from the Launch
	// (Launch.Cell.Workspace); this carrier dies with the plugin arm.
	EnvCellWorkDir = "CTXLOOM_CELL_WORKDIR"
)

// ErrNoReachBack is DecodeReach's refusal: the process environment carries no
// coordinator endpoint.
var ErrNoReachBack = errors.New("sessions: no reach-back endpoint in the process environment")

// EncodeReach renders the reach-back trio for a runner PROCESS.
func EncodeReach(reach Endpoint, runID string) map[string]string {
	return map[string]string{EnvCoordURL: reach.URL, EnvCoordCred: reach.Credential, EnvRunID: runID}
}

// DecodeReach reads the reach-back trio back; a missing URL or credential is
// ErrNoReachBack.
func DecodeReach(getenv func(string) string) (Endpoint, string, error) {
	ep := Endpoint{URL: getenv(EnvCoordURL), Credential: getenv(EnvCoordCred)}
	if ep.URL == "" || ep.Credential == "" {
		return Endpoint{}, "", ErrNoReachBack
	}
	return ep, getenv(EnvRunID), nil
}

// HookEnv renders the identity carriers the ENGINE process forwards to its
// hook subprocesses.
func HookEnv(id Identity) map[string]string {
	return map[string]string{EnvHarp: id.Harp, EnvProjectID: id.Project}
}

// DecodeHookEnv reads the identity carriers back and applies the harp rule.
func DecodeHookEnv(getenv func(string) string) (Identity, error) {
	id := Identity{Harp: getenv(EnvHarp), Project: getenv(EnvProjectID)}
	return id, id.Validate()
}
