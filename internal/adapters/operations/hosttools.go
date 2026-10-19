package operations

// The host-relayed tools' CONTRACT: the argument shape and the description
// each advertises. The set is derived — every tool that reads the sessions
// root or the task store's history — and it has two servers today
// that must describe each tool identically: the stdio server, which answers
// in-process, and the runner's endpoint, which relays each call to the
// coordinator's Host verb as a HostRequest{Tool, Args}. The shape lives here,
// once, beside the application services that answer it.

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
	ContextStatusDesc    = "Measure how full this session's context window actually is, instead of estimating it. Returns the most recent recorded sample (percent used, tokens in the window, window size) plus a short trend of earlier samples so the DIRECTION is visible, not just the level. Call this the moment a conversation starts to feel long — before winding down, compacting, splitting work off to a subagent, or telling the user you are running low: this turns that hunch into a number. Samples are captured by ctxloom's statusline integration as the session runs. When no samples exist the tool says so explicitly and returns NO percentage — an absent measurement, never a zero one, because a reported 0% would be indistinguishable from an empty context."
	EvaluateTriggersDesc = "Evaluate every Deferred task's revive trigger against gathered evidence (git history since the task was deferred, changed files, and the status of other tasks) and return a machine verdict per trigger: fired, not-fired, needs-investigation, or cannot-determine. The batch is triaged in bounded chunks against the fast model, not one call for everything, so a large Deferred backlog does not silently lose tasks off the end of an oversized response. A needs-investigation verdict may resolve itself internally — ctxloom can run one bounded, whitelisted follow-up look (file existence, a read-only grep, recent commits on a path, another task's status) before settling a final verdict, without any extra turns on your part. This is TRIAGE ONLY — it proposes verdicts for a human to confirm and never changes any task's status itself. Use it before check-triggers' own judgement, or to get a second, evidence-grounded opinion on a Deferred task's trigger. A \"cannot-determine\" verdict means the trigger genuinely cannot be judged from evidence available inside this system (e.g. it depends on something a person has to say) — treat it the same as \"not yet\", never as \"fired\". If the model dropped any task from its response despite chunking, the \"omitted\" count says so explicitly — those tasks still get a cannot-determine verdict, but the count tells you it was a drop, not a genuine judgment; rerun with refresh=true to retry them. Verdicts are cached against the evidence that produced them (see the \"cached\" field per verdict); pass refresh=true to force a fresh look."
)
