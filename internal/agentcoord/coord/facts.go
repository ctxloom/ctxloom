package coord

import (
	"encoding/json"
	"fmt"
	"time"
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
	// factRunHarness binds the run to its harness-native session id (the
	// resume handle RunExited may carry).
	factRunHarness = "run.harness"
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
	// lists a harp's current run (the resume key lives there).
	factRunReaped = "run.reaped"
	// factSessionCred registers a session-owner (depth-0) credential: the
	// parent harness's identity. Only the SHA-256 of the token is recorded.
	factSessionCred = "session.cred"
	// factSessionCredRevoked revokes a session-owner credential.
	factSessionCredRevoked = "session.cred.revoked"

	// factMailQueued is one queued mailbox message (durable, role-addressed;
	// deduped on message_id).
	factMailQueued = "mail.queued"
	// factMailConsumed advances a role's cursor: the listed message_ids are
	// consumed. Appended only when a delivery is acknowledged — a SUBSEQUENT
	// agent_recv from the same role (cursor-ack), or a turn-boundary
	// delivery into a child the coordinator itself drives.
	factMailConsumed = "mail.consumed"
)

// Terminal causes recorded on factRunEnded.
const (
	// CauseChatClose is the legacy chat-stream-close path (endChild): the
	// child's engine event stream ended. Host children's only signal in B1.
	CauseChatClose = "chat-close"
	// CauseRunnerLoss is the coordinator-side synthesis: the
	// run's RunnerChannel disconnected or missed heartbeats past the bound.
	CauseRunnerLoss = "runner-loss"
	// CauseRunnerExit is an explicit RunExited frame from the runner.
	CauseRunnerExit = "runner-exit"
	// CauseStopped is an agent_stop (KillRun).
	CauseStopped = "stopped"
	// CauseLaunchFailed is a child that never came up.
	CauseLaunchFailed = "launch-failed"
	// CauseOrphaned marks runs adopted from disk after a coordinator
	// relaunch: their engine died with the previous process. Queued mail is
	// preserved; a later send/inject resumes the harp as a fresh run.
	CauseOrphaned = "orphaned-by-restart"
	// CauseOneShotBoundary is a driving:oneshot child's turn-boundary teardown
	// (one-shot-resume plan, Slice 4): the turn completed cleanly, so the
	// engine process is torn down and the harp left RESUMABLE — the next
	// mailbox delivery resumes it by native session key (session/load), a
	// fresh turn. It is a NON-error, EXPECTED terminal that repeats every turn,
	// so unlike every other cause it queues NO "exited" notice to the parent
	// (the turn's result was already bridged) and — like every cause except
	// CauseStopped — it must NOT clear the harp's ACCEPT_FOR_SESSION grants.
	CauseOneShotBoundary = "oneshot-boundary"
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
	ParentRunID string `json:"parent_run_id,omitempty"`
	Runtime     string `json:"runtime,omitempty"` // resolved runtime axis ("", "host", "container-rootless", "container-rootful")
	CredHash    string `json:"cred_hash"`         // hex SHA-256 of the bearer token — never the token
	Depth       int    `json:"depth"`
	// OneShot mirrors this run's own SpawnPlan.ResumeMode ==
	// ResumeModeOneShot — journaled so this run's OWN future credential
	// (folds.go's applyEnqueued) reports it via Identity.OneShot without a
	// second resolve. See Identity.OneShot's doc for what it gates.
	OneShot bool   `json:"one_shot,omitempty"`
	Prompt  string `json:"prompt,omitempty"` // briefing (journal is 0600, like the mailbox)
	Resume  bool   `json:"resume,omitempty"` // a re-attempt for an ended harp
	// Ladder is the run's resolved escalation ladder (Wave C2), journaled at
	// enqueue so a later config edit cannot retroactively change a live
	// run's policy and restart adoption recovers it without re-resolving
	// config. Kind names, not wire numbers, so runs.jsonl stays
	// jq-legible; ladder.go owns the (Ladder <-> []ladderRungFact)
	// conversion.
	Ladder []ladderRungFact `json:"ladder,omitempty"`
	// Permission is the child's resolved permission mode's kind name
	// (agent.PermissionMode.String(): "bypass"|"plan"|"default"|
	// "acceptEdits") — Wave F1, journaled at enqueue
	// for the SAME reason as Ladder: a later config edit must not
	// retroactively change what a live run's privileges were. Kind name, not
	// a wire number, so runs.jsonl stays jq-legible.
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

// ladderRungFact is LadderRung's durable JSON projection.
type ladderRungFact struct {
	Kinds   []string `json:"kinds,omitempty"`
	Action  string   `json:"action"`
	Role    string   `json:"role,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
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

// runHarness is factRunHarness's payload.
type runHarness struct {
	RunID            string `json:"run_id"`
	HarnessSessionID string `json:"harness_session_id"`
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
	Project  string `json:"project,omitempty"`
	CredHash string `json:"cred_hash"`
}

// mailQueued is factMailQueued's payload.
type mailQueued struct {
	MessageID  string          `json:"message_id"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Kind       string          `json:"kind,omitempty"`
	Body       string          `json:"body"`
	Structured json.RawMessage `json:"structured,omitempty"`
	InReplyTo  string          `json:"in_reply_to,omitempty"`
}

// mailConsumed is factMailConsumed's payload.
type mailConsumed struct {
	Role       string   `json:"role"`
	MessageIDs []string `json:"message_ids"`
}

// interaction is the audit payload for the interaction journal: one record
// per resolved delegation interaction (agent_run/send/recv/stop, injections,
// runner loss syntheses), plan-1 InteractionRecorded's ancestor.
type interaction struct {
	Kind   string            `json:"kind"`
	Actor  string            `json:"actor,omitempty"` // caller harp
	Detail map[string]string `json:"detail,omitempty"`
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
