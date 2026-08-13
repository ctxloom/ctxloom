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
	// runner, so RunnerChannel Hello correlates the runner to the run the
	// coordinator will StartRun on it (plan A9). Absent on the
	// parent-session credential.
	EnvRunID = "CTXLOOM_RUN_ID"
	// EnvMCPSocket is the container-local (or host user-private) unix
	// socket path of the RUNNER's MCP endpoint. The runner creates the
	// socket BEFORE the harness spawns and exports this into the harness
	// env; a `ctxloom mcp` shim finding it forwards the whole surface
	// there (HTTP-over-unix — the tool path never crosses the container
	// boundary; the runner is the one credential holder and the one
	// egress).
	//
	// One reach-back seam encodes a SECOND value shape here: the ACP
	// editor driver's container transport (internal/acp/container_transport.go)
	// cannot bind-mount a live unix socket across the Docker Desktop VM
	// boundary off Linux (macOS/Windows), so there this instead carries
	// "tcp://host:port" — a host-loopback TCP bridge onto the same unix
	// socket, dialed by the shim's forward mode (internal/mcp/mcp_forward.go).
	// Every OTHER setter of this var (this package's runner, in particular)
	// always emits a plain unix path; only that one ACP seam ever emits the
	// tcp:// form.
	EnvMCPSocket = "CTXLOOM_MCP_SOCKET"
	// EnvRunDepth carries this run's DELEGATION DEPTH to its runner process:
	// "0" for the session owner, "1" for its directly-spawned subagents,
	// "2" for theirs, and so on — a general counter, not an owner/child bit.
	// It rides the SAME per-spawn runnerEnv seam as the trio above — never
	// the wire (RunnerEnv is an untyped map[string]string; no proto change)
	// — and is stamped UNCONDITIONALLY (unlike the trio, which is omitted
	// whole when url == ""): leafness must not depend on reach-back. The
	// runner (internal/cli/standUpRunner/attachRunnerMCP) reads it, defaulting
	// unset/empty/unparseable to 0, and compares it against the resolved
	// config.Config.GetDelegationDepth() cap to decide whether this session
	// is a LEAF (depth >= cap) and gate the coordinator-only MCP tools
	// accordingly (mcp_runner.go). Replaces the retired per-agent
	// `coordinator` bool (EnvAgentCoordinator): leafness is now a STRUCTURAL
	// property of the run (its position in the delegation tree), not a
	// per-agent trust flag.
	EnvRunDepth = "CTXLOOM_RUN_DEPTH"
	// EnvRunOneShot carries whether THIS run's own resolved plan is
	// ResumeModeOneShot ("true") or not ("false" — also the default for
	// absent or any unparseable value). Rides the same seam as EnvRunDepth, stamped
	// UNCONDITIONALLY for the identical reason: leafness must not depend on
	// reach-back. A one-shot run is ALWAYS a leaf, regardless of depth —
	// its engine tears down at every turn boundary, so it cannot hold a
	// coordination relationship across turns (see Identity.OneShot's doc).
	EnvRunOneShot = "CTXLOOM_RUN_ONESHOT"
	// EnvRunSpoolTee carries whether the mailbox's SHADOW TEE onto the file
	// spool is on for this run ("true"), so the runner's outbound half
	// (Home.teePeerSendResponse) and the coordinator's inbound half agree
	// without asking each other. Rides the same per-spawn runnerEnv seam as
	// EnvRunDepth/EnvRunOneShot and is stamped UNCONDITIONALLY for the same
	// reason those are: the runner must be able to tell "the tee is off" from
	// "the stamp went missing", and both read as off only because off is the
	// safe direction — an un-teed run loses shadow coverage, never mail.
	//
	// The coordinator is the only source. A runner cannot read the project
	// config for this the way it does for delegation.depth, because a
	// containerized runner's config is a different file (or none), and a run
	// teed on one side only would produce exactly the half-populated spool the
	// soak would then misread as a mapping bug.
	EnvRunSpoolTee = "CTXLOOM_RUN_SPOOL_TEE"
	// EnvRunSpoolDelivery carries whether coordinator<->child mail for THIS
	// run is DELIVERED from the file spool ("true") rather than the mailbox.
	// It rides the same per-spawn runnerEnv seam as EnvRunSpoolTee and is
	// stamped just as unconditionally, but the consequence of the two ends
	// disagreeing is far worse here than for the tee: a coordinator that
	// writes only files to a runner still reading the mailbox delivers
	// NOTHING, with every signal green. The stamp is the single source so
	// that disagreement is impossible by construction rather than by two
	// processes independently resolving the same config — which a
	// containerized runner cannot do at all (its config is a different file,
	// or none).
	//
	// Anything other than exactly "true" reads as OFF, which is the safe
	// direction: off means the mailbox, which works whatever the other end
	// believes.
	EnvRunSpoolDelivery = "CTXLOOM_RUN_SPOOL_DELIVERY"
	// EnvCellWorkDir carries the prepared workspace directory
	// (isolation.Workspace.Dir(), e.g. Worktree's per-agent checkout) to the
	// runner process at spawn time. It rides the SAME per-spawn spawnEnv
	// seam as the trio above (internal/lm/isolation/none.go's SpawnClient
	// choke point) — never the wire.
	//
	// It exists to close a host+worktree discovery-key mismatch
	// (fix/host-discovery-anchor): the runner (`ctxloom llm serve`) is
	// spawned with NO cmd.Dir (internal/lm/grpc/client.go), so it inherits
	// the COORDINATOR's cwd — but the engine harness it hosts is launched
	// with cmd.Dir=spec.WorkDir (internal/lm/backends/launcher.go), the
	// per-agent WORKTREE. The runner's `ctxloom mcp` discovery marker used
	// to key itself off its OWN os.Getwd() (the coordinator's cwd), while
	// the shim keys off ITS cwd (the worktree) — for workspace:worktree
	// these differ, so discovery misses and a delegated child can't reach
	// its parent. The runner (internal/cli/llm_serve.go) reads this var and
	// passes it into ServeRunnerMCP so the marker is keyed by the SAME
	// workspace directory the shim's cwd derives from
	// (internal/mcp/mcp_runner.go), falling back to the runner's own
	// os.Getwd() when this is unset (workspace:none / container, where
	// runner cwd and child WorkDir already agree, or a container, which
	// uses a fixed marker name and never consults this at all).
	EnvCellWorkDir = "CTXLOOM_CELL_WORKDIR"
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
	// OneShot mirrors THIS run's own SpawnPlan.ResumeMode ==
	// ResumeModeOneShot (a `driving: oneshot` agent on a resume-capable,
	// wired backend — spawner.go's resolveResumeMode): its engine tears
	// down and is resumed by native session key at every turn boundary, so
	// it cannot hold a coordination relationship across turns. AgentRun's
	// "may this run spawn" guard refuses a OneShot caller outright,
	// regardless of Depth — a one-shot run's effective spawn budget is
	// zero. UNRELATED to childRt.oneshot/OwnerRunSpec.Oneshot, a completely
	// different axis: a --print single-turn CLI session tearing down after
	// one answer, never journaled onto Identity.
	OneShot bool `json:"one_shot,omitempty"`
	// Project is the project directory the coordinator serves.
	Project string `json:"project,omitempty"`
	// Consumer marks a D1 read-only watch credential (coord/consumer.go): it
	// authenticates ConsumerService (WatchRuns/ListRuns) ONLY — the gRPC auth
	// interceptor rejects it on every CoordinatorService method (RunnerChannel/
	// RunChannel/PublishEvents), so a leaked viewer credential cannot mutate
	// anything or impersonate a runner/child. Never journaled: minted fresh
	// per coordinator process, verified in-memory only (creds.go).
	Consumer bool `json:"-"`
}

// IsChild reports whether this identity belongs to a spawned child run.
func (id Identity) IsChild() bool { return id.Depth > 0 }
