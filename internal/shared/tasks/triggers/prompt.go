package triggers

import (
	"fmt"
	"strings"
)

// timeLayout is the human-readable stamp used throughout the prompt — date
// precision for evidence, seconds precision for the evaluation clock.
const dateLayout = "2006-01-02"
const timeLayout = "2006-01-02T15:04:05Z"

// BuildPrompt constructs the single batch-triage prompt for b: framing, every
// Deferred task's evidence, a bounded cross-reference of other tasks, and the
// exact JSON contract the response must satisfy. One prompt judges every
// trigger in the batch in one call — see the ctxloom-side evaluator for why
// (one LLM invocation per check-triggers run, not one per task).
func BuildPrompt(b Batch) string {
	var sb strings.Builder

	sb.WriteString("You are triaging revive triggers on Deferred tasks in a project's task tracker.\n")
	sb.WriteString("Each Deferred task carries a free-text TRIGGER: a natural-language condition that, once true, means the task should come back to the active list.\n")
	sb.WriteString("You are given evidence gathered from the repository (commits since the task was deferred, changed files) and the current status of other tasks. Judge each trigger against ONLY this evidence — do not assume anything not shown here.\n\n")

	sb.WriteString("For EACH task, choose exactly one outcome:\n")
	fmt.Fprintf(&sb, "- %q: the evidence shows the trigger condition has occurred.\n", string(Fired))
	fmt.Fprintf(&sb, "- %q: the evidence shows the condition has not occurred yet.\n", string(NotFired))
	fmt.Fprintf(&sb, "- %q: the evidence is suggestive but not conclusive; a closer, targeted look could resolve it. You MAY attach a short list of evidence QUERIES to this verdict (see below) — ctxloom will run them and ask you again with the results.\n", string(NeedsInvestigation))
	fmt.Fprintf(&sb, "- %q: the trigger cannot be judged from evidence available inside this system (e.g. it depends on a person's statement, or an event with no trace in the repository or task store). USE THIS rather than guessing — a wrong %q verdict revives a task that is not actually ready, which is worse than leaving it parked.\n\n", string(CannotDetermine), string(Fired))

	writeQueryProtocol(&sb)

	// The mirror hazard to a false fired. Left unsaid, a model reads a silent
	// evidence pack as proof the condition never occurred and returns a
	// confident not-fired — which parks a genuinely fired task forever.
	sb.WriteString("CRITICAL — Absence of evidence is not evidence of absence.\n")
	fmt.Fprintf(&sb, "Only answer %q when the evidence gives POSITIVE reason to believe the condition has NOT occurred (e.g. the repository state shows the thing plainly missing, or a related task is still open).\n", string(NotFired))
	fmt.Fprintf(&sb, "If the evidence pack simply does not SPEAK to the trigger — it is silent, not contradictory — that is %q or %q, NEVER %q. Do not reason \"I was shown nothing about X, therefore X did not happen\".\n\n", string(NeedsInvestigation), string(CannotDetermine), string(NotFired))

	if !b.Now.IsZero() {
		fmt.Fprintf(&sb, "Evaluation time: %s\n\n", b.Now.UTC().Format(timeLayout))
	}

	writeRepoState(&sb, b.Repo)

	sb.WriteString("=== Deferred tasks ===\n\n")
	for _, t := range b.Tasks {
		writeTaskEvidence(&sb, t)
	}

	if len(b.OtherTasks) > 0 {
		sb.WriteString("=== Other tasks (for cross-reference) ===\n\n")
		for _, o := range b.OtherTasks {
			fmt.Fprintf(&sb, "- [%s] %s: %s\n", o.Status, o.HarpID, o.Text)
		}
		sb.WriteString("\n")
	}

	writeResponseContract(&sb, true)

	return sb.String()
}

// writeQueryProtocol explains the bounded, whitelisted follow-up evidence a
// model may request on a needs-investigation verdict. It names every
// QueryType explicitly (rather than pointing at "the query package") because
// the prompt IS the contract the model reads to decide what it's allowed to
// ask for — a closed set spelled out in full, not an open-ended capability.
func writeQueryProtocol(sb *strings.Builder) {
	sb.WriteString("=== Evidence queries (needs-investigation only) ===\n\n")
	sb.WriteString("If, and only if, a task's outcome is \"needs-investigation\", you may attach a \"queries\" array of up to a few targeted follow-up requests. Each is one of exactly these shapes — nothing else is supported, and anything else is dropped:\n")
	fmt.Fprintf(sb, "  {\"type\": %q, \"path\": \"repo/relative/path\"} — does this file or directory exist?\n", string(QueryPathExists))
	fmt.Fprintf(sb, "  {\"type\": %q, \"pattern\": \"text or regex\", \"path_glob\": \"optional\"} — bounded read-only search of file CONTENTS. With no path_glob it searches the whole repository, which is usually what you want; only add path_glob (a glob such as \"**/*.go\", or a directory) to narrow it, and only when you are certain the thing you seek could not live elsewhere.\n", string(QueryGrep))
	fmt.Fprintf(sb, "  {\"type\": %q, \"path\": \"repo/relative/path\"} — recent commits touching this path.\n", string(QueryGitLogPath))
	fmt.Fprintf(sb, "  {\"type\": %q, \"harp_id\": \"other-task-harp-id\"} — current status of another task.\n", string(QueryTaskStatus))
	sb.WriteString("Every path MUST be relative to the repository root — no leading \"/\", no \"..\". This is round 1 of exactly two: you get ONE follow-up look with the query results, then you must give a final verdict — needs-investigation is not a valid answer in round 2.\n\n")
}

// BuildFollowupPrompt constructs round 2 — the escalation round — for the
// tasks in b: the SAME per-task evidence round 1 saw, plus the results of the
// queries the model asked for, and an explicit instruction that
// needs-investigation is no longer available. This is the LAST look: round 2
// output must be fired, not-fired, or cannot-determine — see the ctxloom-side
// caller for the hard backstop that forces a stray needs-investigation to
// cannot-determine even if a model ignores this instruction.
func BuildFollowupPrompt(b FollowupBatch) string {
	var sb strings.Builder

	sb.WriteString("This is ROUND 2 (the final round) of revive-trigger triage on Deferred tasks. In round 1 you flagged the tasks below as needs-investigation and asked for specific evidence; that evidence is included below.\n")
	sb.WriteString("You must now give a FINAL verdict for each task. needs-investigation is NOT a valid outcome this round — if the new evidence still doesn't settle it, answer \"cannot-determine\" rather than asking for more.\n\n")

	sb.WriteString("For EACH task, choose exactly one outcome:\n")
	fmt.Fprintf(&sb, "- %q: the evidence shows the trigger condition has occurred.\n", string(Fired))
	fmt.Fprintf(&sb, "- %q: the evidence shows the condition has not occurred yet.\n", string(NotFired))
	fmt.Fprintf(&sb, "- %q: even with the follow-up evidence, this cannot be judged from what's available inside this system. USE THIS rather than guessing.\n\n", string(CannotDetermine))

	sb.WriteString("CRITICAL — Absence of evidence is not evidence of absence.\n")
	fmt.Fprintf(&sb, "Only answer %q when the evidence gives POSITIVE reason to believe the condition has NOT occurred. If the follow-up evidence is silent rather than contradictory, answer %q, NEVER %q.\n\n", string(NotFired), string(CannotDetermine), string(NotFired))

	if !b.Now.IsZero() {
		fmt.Fprintf(&sb, "Evaluation time: %s\n\n", b.Now.UTC().Format(timeLayout))
	}

	sb.WriteString("=== Tasks under final review ===\n\n")
	for _, t := range b.Tasks {
		writeTaskEvidence(&sb, t.TaskInput)
		writeQueryResults(&sb, t.Results)
	}

	writeResponseContract(&sb, false)

	return sb.String()
}

// writeQueryResults renders the deterministic answers ctxloom gathered for
// one task's round-1 query requests. A query that failed or was rejected
// still gets a line — the model must see that it produced no evidence,
// rather than the request silently vanishing between rounds.
func writeQueryResults(sb *strings.Builder, results []QueryResult) {
	if len(results) == 0 {
		return
	}
	sb.WriteString("Query results requested in round 1:\n")
	for _, r := range results {
		if r.Err != "" {
			fmt.Fprintf(sb, "  - %s: ERROR: %s\n", describeQuery(r.Query), r.Err)
			continue
		}
		out := r.Output
		if out == "" {
			out = "(no matches)"
		}
		fmt.Fprintf(sb, "  - %s: %s\n", describeQuery(r.Query), out)
	}
	sb.WriteString("\n")
}

// describeQuery renders a Query as a short human-readable label for the
// prompt, matching the shape the model itself used to request it.
func describeQuery(q Query) string {
	switch q.Type {
	case QueryPathExists:
		return fmt.Sprintf("path_exists(%s)", q.Path)
	case QueryGrep:
		if q.PathGlob != "" {
			return fmt.Sprintf("grep(%q, %s)", q.Pattern, q.PathGlob)
		}
		return fmt.Sprintf("grep(%q)", q.Pattern)
	case QueryGitLogPath:
		return fmt.Sprintf("git_log_path(%s)", q.Path)
	case QueryTaskStatus:
		return fmt.Sprintf("task_status(%s)", q.HarpID)
	default:
		return string(q.Type)
	}
}

// writeRepoState renders the repo-global "what exists NOW" evidence once, up
// front: the directory inventory and the uncommitted working-tree changes.
// Per-task commit history answers "what changed since this was parked"; this
// answers "does the thing the trigger names exist at all", which history
// alone cannot (the thing may predate the window, or be uncommitted).
func writeRepoState(sb *strings.Builder, r RepoState) {
	if len(r.Dirs) == 0 && len(r.WorkingChanges) == 0 {
		return
	}
	sb.WriteString("=== Repository state right now ===\n\n")
	sb.WriteString("Use this to answer existence-style triggers (\"when X exists\", \"once X ships\"). It reflects the CURRENT working tree, including work that is not yet committed.\n\n")
	if len(r.Dirs) > 0 {
		sb.WriteString("Directories present in the repository:\n")
		for _, d := range r.Dirs {
			fmt.Fprintf(sb, "  %s\n", d)
		}
		sb.WriteString("\n")
	}
	if len(r.WorkingChanges) > 0 {
		sb.WriteString("Uncommitted working-tree changes (in NO commit — commit history cannot show these):\n")
		for _, c := range r.WorkingChanges {
			fmt.Fprintf(sb, "  %s\n", c)
		}
		sb.WriteString("\n")
	}
}

func writeTaskEvidence(sb *strings.Builder, t TaskInput) {
	fmt.Fprintf(sb, "Task %s: %s\n", t.HarpID, t.Text)
	fmt.Fprintf(sb, "Trigger: %s\n", t.Trigger)
	if !t.DeferredAt.IsZero() {
		fmt.Fprintf(sb, "Deferred since: %s\n", t.DeferredAt.UTC().Format(dateLayout))
	} else {
		sb.WriteString("Deferred since: unknown\n")
	}
	if len(t.CommitsSince) == 0 {
		sb.WriteString("Commits since deferred: (none gathered)\n")
	} else {
		sb.WriteString("Commits since deferred:\n")
		for _, c := range t.CommitsSince {
			fmt.Fprintf(sb, "  - %s %s %s\n", shortSHA(c.SHA), c.Date.UTC().Format(dateLayout), c.Subject)
		}
	}
	if len(t.ChangedFiles) > 0 {
		fmt.Fprintf(sb, "Changed files: %s\n", strings.Join(t.ChangedFiles, ", "))
	}
	sb.WriteString("\n")
}

// writeResponseContract emits the response contract both rounds share. Round 1
// offers needs-investigation and its queries field; round 2, the final look,
// does not. Everything else is one wording: two wordings is two chances for a
// model to answer in a shape the parser rejects.
func writeResponseContract(sb *strings.Builder, allowInvestigation bool) {
	outcomes := "fired|not-fired|cannot-determine"
	queries := ""
	if allowInvestigation {
		outcomes = "fired|not-fired|needs-investigation|cannot-determine"
		queries = `, "queries": [optional, needs-investigation only, see above]`
	}
	sb.WriteString("=== Response format ===\n\n")
	sb.WriteString("Respond with ONLY a JSON array, one object per task listed above, no prose before or after, no markdown code fence. Each object:\n")
	fmt.Fprintf(sb, `{"harp_id": "...", "outcome": %q, "evidence": ["..."], "reasoning": "one or two sentences"%s}`+"\n", outcomes, queries)
	sb.WriteString("Include every task listed above, using its exact harp_id. Do not invent tasks or harp ids.\n")
}

// shortSHA trims a commit hash to a readable prefix for the prompt (evidence
// only needs enough to be recognizable, not the full 40 chars).
func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
