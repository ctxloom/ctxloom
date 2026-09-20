package operations

// The host-relayed tools' CONTRACT: the argument shape and the description
// each of the seven advertises. The set is derived — every tool that reads
// the sessions root or cross-session history — and it has two servers today
// that must describe each tool identically: the stdio server, which answers
// in-process, and the runner's endpoint, which relays each call to the
// coordinator's Host verb as a HostRequest{Tool, Args}. The shape lives here,
// once, beside the application services that answer it.

// CompactSessionInput is the argument shape of compact_session.
type CompactSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Session ID to compact (defaults to current session)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation (defaults to config or claude-3-haiku)"`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to the configured default LLM)"`
}

// LoadSessionInput is the argument shape of load_session.
type LoadSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Backend-native session ID (UUID). Either session_id or harp_name is required."`
	HarpName  string `json:"harp_name,omitempty" jsonschema:"Harp-named session reference (e.g. \"swift-amber-falcon\") naming a directory under ~/.ctxloom/sessions. Resolved to a session_id via that session's record; if both are passed, harp_name wins."`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to the configured default LLM)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation if needed"`
}

// RecoverSessionInput is the argument shape of recover_session.
type RecoverSessionInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"Session ID to recover. If not provided, resolves this session's own transcript by harp identity."`
	Backend   string `json:"backend,omitempty" jsonschema:"Backend to read session from (defaults to the configured default LLM)"`
	Model     string `json:"model,omitempty" jsonschema:"LLM model to use for distillation if needed"`
}

// GetPreviousSessionInput is the argument shape of get_previous_session.
type GetPreviousSessionInput struct {
	Model string `json:"model,omitempty" jsonschema:"LLM model to use for distillation if needed"`
}

// ListSessionsInput is the argument shape of list_sessions.
type ListSessionsInput struct {
	AllProjects    bool `json:"all_projects,omitempty" jsonschema:"List sessions from every project instead of only the current working directory's project (mirrors session list --all)"`
	DistillMissing bool `json:"distill_missing,omitempty" jsonschema:"Distill sessions whose essence is missing or stale before listing, so every row carries a title. Runs the compactor out of band; canonical-transcript sessions distill, legacy-only sessions are skipped."`
}

// ContextStatusInput is the argument shape of context_status.
type ContextStatusInput struct {
	Trend int `json:"trend,omitempty" jsonschema:"How many of the most recent samples to return as a trend, oldest first (default 10, maximum 100)"`
}

// EvaluateTriggersInput is the argument shape of evaluate_triggers.
type EvaluateTriggersInput struct {
	MaxCommits int  `json:"max_commits,omitempty" jsonschema:"Per-task cap on how many commits (since the task was deferred) are gathered as evidence. Default 20."`
	Refresh    bool `json:"refresh,omitempty" jsonschema:"Bypass the verdict cache and re-evaluate every Deferred task's trigger against the model, even if nothing about its evidence changed since the last call."`
}

// The tool descriptions, byte-identical on both servers.
const (
	CompactSessionDesc     = "Distil a session's PERSISTED transcript into a summary on disk, for a LATER session to pick up. Reads the stored log in a separate process; it does NOT touch your live conversation and frees no context in it. Do NOT call this because you are running low on context — for that, use your harness's native compaction. This exists precisely so that a context-starved agent never has to write its own summary: it runs out of band, against the full transcript, with a fresh budget. Normally you do not call it at all — it runs on shutdown, at startup for historical sessions, and on recovery after a /clear. Call it explicitly only to force an essence before ending a session."
	ListSessionsDesc       = "List harp-named sessions with their title, backend, last-activity time, and whether they're distilled — the menu you pick a harp from to hand to load_session. Defaults to the current working directory's project; set all_projects to span every project. Set distill_missing to compact title-less or stale sessions first so every row shows a title."
	LoadSessionDesc        = "Distill and load context from a session. Accepts either session_id (backend UUID) or harp_name (human-readable). For names, see ctxloom://sessions/recent."
	RecoverSessionDesc     = "Recover context from the current session after /clear. Resolves this session's own transcript by harp identity, falling back to the most recent transcript in this working directory only when that transcript cannot be attributed to a different session, and distills it (no session id needed; pass one to target a specific session)."
	GetPreviousSessionDesc = "Distill and load an EARLIER session's content — the most recent session BEFORE the active one for this working directory, resolved via the session registry (cross-agent aware; falls back to the second-most-recent transcript). For inspecting a prior session. NOT the post-/clear path: /clear keeps the SAME session alive, so to recover context wiped by /clear use recover_session instead."
	ContextStatusDesc      = "Measure how full this session's context window actually is, instead of estimating it. Returns the most recent recorded sample (percent used, tokens in the window, window size) plus a short trend of earlier samples so the DIRECTION is visible, not just the level. Call this the moment a conversation starts to feel long — before winding down, compacting, splitting work off to a subagent, or telling the user you are running low: this turns that hunch into a number. Samples are captured by ctxloom's statusline integration as the session runs. When no samples exist the tool says so explicitly and returns NO percentage — an absent measurement, never a zero one, because a reported 0% would be indistinguishable from an empty context."
	EvaluateTriggersDesc   = "Evaluate every Deferred task's revive trigger against gathered evidence (git history since the task was deferred, changed files, and the status of other tasks) and return a machine verdict per trigger: fired, not-fired, needs-investigation, or cannot-determine. The batch is triaged in bounded chunks against the fast model, not one call for everything, so a large Deferred backlog does not silently lose tasks off the end of an oversized response. A needs-investigation verdict may resolve itself internally — ctxloom can run one bounded, whitelisted follow-up look (file existence, a read-only grep, recent commits on a path, another task's status) before settling a final verdict, without any extra turns on your part. This is TRIAGE ONLY — it proposes verdicts for a human to confirm and never changes any task's status itself. Use it before check-triggers' own judgement, or to get a second, evidence-grounded opinion on a Deferred task's trigger. A \"cannot-determine\" verdict means the trigger genuinely cannot be judged from evidence available inside this system (e.g. it depends on something a person has to say) — treat it the same as \"not yet\", never as \"fired\". If the model dropped any task from its response despite chunking, the \"omitted\" count says so explicitly — those tasks still get a cannot-determine verdict, but the count tells you it was a drop, not a genuine judgment; rerun with refresh=true to retry them. Verdicts are cached against the evidence that produced them (see the \"cached\" field per verdict); pass refresh=true to force a fresh look."
)
