package coord

// Env var names of the coordinator reach-back trio injected per spawn on both
// spawn paths (and onto the parent harness by `ctxloom run`/`ctxloom acp`).
// The credential is read from the HARNESS-INHERITED process env only — it is
// never written into any MCP config structure, file, or Env map.
const (
	// EnvCoordURL is the coordinator's MCP endpoint URL
	// (http://host:port/mcp); the gRPC RunnerChannel rides the same
	// host:port (one h2c listener, content-type routed).
	EnvCoordURL = "CTXLOOM_COORD_URL"
	// EnvCoordCred is the caller's bearer token (identity, not just
	// admission). 256-bit, hex; only its SHA-256 is ever persisted.
	EnvCoordCred = "CTXLOOM_COORD_CRED"
	// EnvRunID is the coordinator-minted run id for a spawned child's
	// runner, so RunnerChannel Hello correlates before StartRun exists
	// (plan A9). Absent on the parent-session credential.
	EnvRunID = "CTXLOOM_RUN_ID"
	// EnvMCPSocket is the container-local (or host user-private) unix
	// socket path of the RUNNER's MCP endpoint. The runner creates the
	// socket BEFORE the harness spawns and exports this into the harness
	// env; a `ctxloom mcp` shim finding it forwards the whole surface
	// there (HTTP-over-unix — the tool path never crosses the container
	// boundary; the runner is the one credential holder and the one
	// egress).
	EnvMCPSocket = "CTXLOOM_MCP_SOCKET"
)

// Identity is what a credential authenticates AND identifies: the caller's
// ambient session identity as the coordinator knows it. It derives from the
// credential/connection per request — never from the calling process's env
// (review R: the shared HTTP server must not see the host process env as
// caller identity).
type Identity struct {
	// Harp is the caller's session harp (a child's harp, or the parent
	// session's own harp for the session-owner credential).
	Harp string `json:"harp"`
	// RunID is the spawned run this credential was minted for; empty on
	// session-owner (parent) credentials.
	RunID string `json:"run_id,omitempty"`
	// Depth is the delegation depth: 0 = the session owner, 1 = a spawned
	// child. The recursion guard derives from THIS, not from env.
	Depth int `json:"depth"`
	// Project is the project directory the coordinator serves.
	Project string `json:"project,omitempty"`
}

// IsChild reports whether this identity belongs to a spawned child run.
func (id Identity) IsChild() bool { return id.Depth > 0 }
