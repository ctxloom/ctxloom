package coord

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// Fact kinds. Two journals: the run-registry journal (runs.jsonl — run
// lifecycle, session credentials) and the mailbox journal (mailbox.jsonl —
// queued peer messages and consume cursors). The interaction journal
// (interactions.jsonl) is an audit log with no projection.
const (
	// factRunEnqueued records an agent_run accepted at enqueue: the run is
	// minted (run_id, credential hash) and joins the spawn queue.
	factRunEnqueued = "run.enqueued"
	// factRunState records a §6a state transition
	// (queued|executing|parked|idle) for a live run.
	factRunState = "run.state"
	// factRunEnded is the run's terminal fact — exactly one per run_id,
	// whatever raced to cause it (chat stream close, runner loss,
	// agent_stop, launch failure, restart adoption). The harp stays
	// resumable: a resume enqueues a NEW run for the same harp.
	factRunEnded = "run.ended"
	// factRunContainer binds a container-runtime run to its resolved
	// container name (RunnerHandle.Name) once StartRunner returns — the
	// name cannot be known at enqueue, only after spawn. Never posted for a
	// host-runtime run.
	factRunContainer = "run.container"
	// factRunResumable records the run engine's LIVE resume capability (ACP's
	// initialize-time loadSession bit, surfaced via ChatSessionInfo.Resumable)
	// — the one-shot resume gate's live half (one-shot-resume plan, Slice 4 /
	// Fork 3). Recorded once per run, from the first session event; only a
	// run that is BOTH statically resume-capable (SpawnPlan.ResumeMode) AND
	// live-resumable may tear its engine down at a turn boundary.
	factRunResumable = "run.resumable"
	// factRunReaped drops the listed (ended, non-current) run records from the
	// live folds — the one-shot retention reap (one-shot-resume plan, Slice 4 /
	// Fork 2.3). One-shot mints one ended run per turn per harp; without a
	// reap the in-memory run/state maps grow unbounded over a long session.
	// A durable fact (not a bare in-memory eviction) so a replay/reconciliation
	// reaches the SAME bounded projection the live coordinator held. NEVER
	// lists a harp's current run (a resume is claimed against it).
	factRunReaped = "run.reaped"
	// factSessionCred registers a session-owner (depth-0) credential: the
	// parent harness's identity. Only the SHA-256 of the token is recorded.
	factSessionCred = "session.cred"
	// factSessionCredRevoked revokes a session-owner credential.
	factSessionCredRevoked = "session.cred.revoked"
)

// Approval fact kinds, journaled in the run-registry journal beside the runs
// they belong to. Every request a run parks for the human and every decision
// on it is recorded; the grant facts are also the grants fold's whole input,
// so a restarted coordinator rebuilds each harp's session grants from them.
const (
	// factApprovalParked records a request parked for the root human.
	factApprovalParked = "approval.parked"
	// factApprovalDecided records how a parked request was resolved, and by
	// whom — exactly one per parked request.
	factApprovalDecided = "approval.decided"
	// factGrantAdded records one allow-for-session rule granted to a harp.
	factGrantAdded = "grant.added"
	// factGrantRevoked withdraws one grant.
	factGrantRevoked = "grant.revoked"
	// factPlanApproved records an approved plan and the posture it executes
	// under.
	factPlanApproved = "plan.approved"
)

// Terminal causes recorded on factRunEnded.
const (
	// CauseRunnerLoss is the coordinator-side synthesis: the
	// run's RunnerChannel disconnected or missed heartbeats past the bound.
	CauseRunnerLoss = "runner-loss"
	// CauseRunnerExit is an explicit RunExited frame from the runner.
	CauseRunnerExit = "runner-exit"
	// CauseStopped is an agent_stop (KillRun).
	CauseStopped = "stopped"
	// CauseLaunchFailed is a child that never came up.
	CauseLaunchFailed = "launch-failed"
	// CauseIdleReaped is the idle reaper's terminal: the run's runner had no
	// turn for delegation.idle_timeout and was ended to free its slot, its
	// process (a container, on that axis) and its bound endpoint. It is an
	// EXPECTED, non-error terminal that leaves the harp RESUMABLE — the next mail starts a new incarnation through the
	// resume arm, reusing the bound endpoint — queues NO "exited" notice to
	// the parent.
	CauseIdleReaped = "idle-reaped"
	// CauseDrained is a child ended by the coordinator's DRAIN at a point
	// where no work was cut short: at its own turn boundary (the exit the
	// drain REQUESTED, honoured), between turns, or before it ever started.
	// Like CauseStopped it is not resumable by leftover mail — drain is
	// shutdown, not supervision — but unlike it, it is not an operator's
	// judgement on the child.
	CauseDrained = "drained"
	// CauseDrainInterrupted is a child whose turn was still running when the
	// drain bound elapsed and was FORCED down. The accepted cost of the bound:
	// a productive turn longer than the bound is interrupted and reported as
	// interrupted, never as a clean exit.
	CauseDrainInterrupted = "drain-interrupted"
	// CauseFinalReported is a child ended because it filed the COMPLETION
	// CONTRACT — a SCOPE_FINAL report — and therefore has nothing left to do
	// (drain.go, endOnFinalReport). Before this, nothing acted on FINAL: the
	// run stayed live, and idle children sat holding their containers (and the
	// worktrees those containers bind-mount) until a human happened to look.
	//
	// It ends the RUN, not the SESSION. Like every cause except CauseStopped
	// it leaves the harp RESUMABLE — leftover mail resumes it (terminateRun's
	// own tail) and a later agent_send resumes it as a fresh run by native
	// session key. Unlike the idle reaper's terminal it DOES queue the
	// parent's "exited" notice: the notice is the parent's signal that the
	// report it just received was the last word.
	CauseFinalReported = "final-reported"
)

// runEnqueued is factRunEnqueued's payload.
type runEnqueued struct {
	RunID      string `json:"run_id"`
	Harp       string `json:"harp"`
	Agent      string `json:"agent"`
	ParentHarp string `json:"parent_harp,omitempty"`
	// ParentRunID is the spawning run's run_id — set only when the caller
	// has a run of its own to be a parent of. That is true for any
	// already-delegated child spawning a further child, AND for a container
	// top-level session's owned run (StartOwnedRun) spawning its first
	// child: an owned run carries a run id of its own from the moment it
	// starts, even though it is depth 0. It is empty only for a child
	// spawned directly by the plugin-hosted top-level session, whose own
	// credential carries no run id at all.
	ParentRunID string             `json:"parent_run_id,omitempty"`
	Runtime     launch.RuntimeAxis `json:"runtime,omitempty"` // resolved runtime axis
	CredHash    string             `json:"cred_hash"`         // hex SHA-256 of the bearer token — never the token
	Depth       int                `json:"depth"`
	// OneShot mirrors this run's own SpawnPlan.ResumeMode ==
	// ResumeModeOneShot — journaled so this run's OWN future credential
	// (folds.go's applyEnqueued) reports it via Identity.OneShot without a
	// second resolve. See Identity.OneShot's doc for what it gates.
	OneShot bool   `json:"one_shot,omitempty"`
	Prompt  string `json:"prompt,omitempty"` // briefing (journal is 0600, like the mailbox)
	Resume  bool   `json:"resume,omitempty"` // a re-attempt for an ended harp
	// Permission is the posture the run was ENQUEUED with: the binding's
	// declared posture for a delegated child (empty when it declared none),
	// the launch's floored posture for an owner run. The effective posture a
	// child runs at is decided once, by the launch resolver, when the run
	// starts; journaled here so a later config edit cannot retroactively
	// change what a live run was asked for. Kind name, not a wire number,
	// so runs.jsonl stays jq-legible.
	Permission string `json:"permission,omitempty"`
	// MCPServers is the child's resolved MCP server NAMES ONLY (Wave F1) —
	// never command, args, or env, which can carry a secret (the SAME
	// boundary CredHash already holds for the bearer token: this journal
	// records identity, never the credential). Composed strictly from the
	// child's OWN resolved profile set (prodSpawner.childMCPServers,
	// spawner.go), so this is the delegation privilege-scoping guarantee's
	// audit trail — a later reader can confirm this run received exactly
	// these servers, never a sibling's or the parent's.
	MCPServers []string `json:"mcp_servers,omitempty"`
}

// runState is factRunState's payload.
type runState struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
}

// runEnded is factRunEnded's payload.
type runEnded struct {
	RunID  string `json:"run_id"`
	Cause  string `json:"cause"`
	Detail string `json:"detail,omitempty"`
}

// runContainer is factRunContainer's payload.
type runContainer struct {
	RunID         string `json:"run_id"`
	ContainerName string `json:"container_name"`
}

// runResumable is factRunResumable's payload.
type runResumable struct {
	RunID     string `json:"run_id"`
	Resumable bool   `json:"resumable"`
}

// runReaped is factRunReaped's payload: the run_ids evicted from the live
// folds by the one-shot retention reap.
type runReaped struct {
	RunIDs []string `json:"run_ids"`
}

// sessionCred is factSessionCred's payload; sessionCredRevoked reuses it
// (CredHash only).
type sessionCred struct {
	Harp     string `json:"harp,omitempty"`
	CredHash string `json:"cred_hash"`
}

// interaction is the audit payload for the interaction journal: one record
// per resolved delegation interaction (agent_run/send/recv/stop, injections,
// runner loss syntheses), plan-1 InteractionRecorded's ancestor.
type interaction struct {
	Kind   string            `json:"kind"`
	Actor  string            `json:"actor,omitempty"` // caller harp
	Detail map[string]string `json:"detail,omitempty"`
}

// approvalParked is factApprovalParked's payload. Input is the tool call's
// input as the engine sent it (the journal is 0600, like the mailbox).
type approvalParked struct {
	ID        ApprovalID      `json:"id"`
	Harp      string          `json:"harp"`
	RunID     string          `json:"run_id,omitempty"`
	Agent     string          `json:"agent,omitempty"`
	Kind      ApprovalKind    `json:"kind"`
	Tool      string          `json:"tool,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Deadline  time.Time       `json:"deadline"`
}

// approvalDecided is factApprovalDecided's payload.
type approvalDecided struct {
	ID      ApprovalID              `json:"id"`
	Harp    string                  `json:"harp"`
	Decider agent.Decider           `json:"decider"`
	Allow   bool                    `json:"allow"`
	Rules   []string                `json:"rules,omitempty"`
	SetMode string                  `json:"set_mode,omitempty"`
	Answers []engine.QuestionAnswer `json:"answers,omitempty"`
	Message string                  `json:"message,omitempty"`
}

// grantAdded is factGrantAdded's payload; the grant's time is the fact's.
type grantAdded struct {
	ID   string     `json:"id"`
	Harp string     `json:"harp"`
	Rule string     `json:"rule"`
	From ApprovalID `json:"from"`
}

// grantRevoked is factGrantRevoked's payload.
type grantRevoked struct {
	ID   string `json:"id"`
	Harp string `json:"harp"`
}

// planApproved is factPlanApproved's payload: the approval that approved the
// plan, the posture it executes under, and the plan's digest and path.
type planApproved struct {
	ID      ApprovalID `json:"id"`
	Harp    string     `json:"harp"`
	Posture string     `json:"posture,omitempty"`
	Digest  string     `json:"digest"`
	Path    string     `json:"path,omitempty"`
}

// factAt builds one journal fact with the command-time timestamp — the ONLY
// place time enters a journal (folds read Fact.At, never time.Now(), so replay
// is deterministic). A marshal failure is a programming error and panics rather
// than dropping state.
func factAt(kind string, at time.Time, payload any) Fact {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("coord: marshal %s fact: %v", kind, err))
	}
	return Fact{Kind: kind, At: at, Data: data}
}
