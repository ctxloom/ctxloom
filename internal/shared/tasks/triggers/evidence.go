package triggers

import "time"

// TaskInput is one Deferred task plus the evidence gathered about it. Every
// field is pre-gathered by the caller — evidence gathering is I/O (reading
// the task store, running git), so it lives ctxloom-side; this package only
// shapes it into a prompt and parses the model's answer back out.
type TaskInput struct {
	HarpID  string
	Text    string
	Trigger string

	// DeferredAt is when the task most recently entered Deferred status (the
	// zero value when unknown). It scopes CommitsSince/ChangedFiles: evidence
	// covers what happened AFTER the task was parked, not the project's whole
	// history.
	DeferredAt time.Time

	// CommitsSince and ChangedFiles are bounded by the caller (evidence
	// gathering); this package never re-truncates them, so what's here is
	// exactly what lands in the prompt.
	CommitsSince []CommitSummary
	ChangedFiles []string
}

// CommitSummary is one commit's evidence: enough to judge a trigger without
// carrying a full diff into the prompt.
type CommitSummary struct {
	SHA     string
	Date    time.Time
	Subject string
}

// OtherTask is a bare status snapshot of a task NOT under evaluation, given
// as cross-referencing evidence (e.g. "task X mentions the same feature and
// is now Done").
type OtherTask struct {
	HarpID string
	Text   string
	Status string
}

// RepoState is what the repository looks like RIGHT NOW, as opposed to what
// changed since a task was deferred. It is repo-global (not per-task), so it
// is rendered once in the prompt header rather than repeated per task.
//
// It exists because a whole class of trigger — "when the signing CLI ships",
// "once package X exists", "when the migration lands" — is an EXISTENCE
// question, and existence is not answerable from commit history: the thing
// may have been committed before the task was deferred (outside the window),
// or may exist uncommitted in the working tree (in no commit at all). Without
// this the model reads a silent evidence pack as proof the condition never
// occurred and returns a confident, wrong not-fired.
type RepoState struct {
	// Dirs is a bounded inventory of directories that exist in the repo
	// (tracked content plus untracked working-tree paths) — enough to answer
	// "does package/path X exist" without shipping a full file listing.
	Dirs []string
	// WorkingChanges is the bounded porcelain status of the working tree
	// (e.g. "?? internal/foo/bar.go", " M internal/baz.go"): work that exists
	// but is in no commit, which commit history structurally cannot show.
	WorkingChanges []string
	// DirsTruncated / WorkingChangesTruncated mark a list that hit its bound
	// and therefore does NOT speak to the whole repository. Both bounds cut
	// ALPHABETICALLY, so the tail of the repo is what disappears — silently
	// rendering such a list as complete invites exactly the confident wrong
	// not-fired this evidence exists to prevent ("internal/signing is absent,
	// so it was never built"). Absence from a truncated list is not evidence
	// of absence, and the prompt must say so.
	DirsTruncated           bool
	WorkingChangesTruncated bool
}

// Batch is the full input to one batch-triage call: every Deferred task's
// evidence pack, a bounded snapshot of other tasks' status, the repo's
// current state, and the evaluation time. Now is injected rather than read
// from time.Now() inside the prompt builder, so the same Batch always renders
// the same prompt.
type Batch struct {
	Tasks      []TaskInput
	OtherTasks []OtherTask
	Repo       RepoState
	Now        time.Time
}

// FollowupTask is one round-1 needs-investigation task carried into round 2:
// its original evidence (so the model doesn't lose context between rounds)
// plus the deterministic results of whichever whitelisted queries it asked
// for and ctxloom executed.
type FollowupTask struct {
	TaskInput
	Results []QueryResult
}

// FollowupBatch is the full input to round 2 — the escalation round. It only
// ever carries the subset of tasks round 1 flagged needs-investigation AND
// gave at least one valid query for; everything else was already settled in
// round 1 and never reaches round 2.
type FollowupBatch struct {
	Tasks []FollowupTask
	Now   time.Time
}
