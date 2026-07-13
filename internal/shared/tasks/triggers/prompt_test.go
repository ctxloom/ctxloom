package triggers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func sampleBatch() Batch {
	return Batch{
		Now: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
		Tasks: []TaskInput{
			{
				HarpID:     "swift-amber-falcon",
				Text:       "wire the signing CLI",
				Trigger:    "when the signing CLI ships",
				DeferredAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
				CommitsSince: []CommitSummary{
					{SHA: "abcdef1234567890", Date: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), Subject: "feat(signing): ship the CLI"},
				},
				ChangedFiles: []string{"internal/signing/cli.go", "internal/signing/cli_test.go"},
			},
			{
				HarpID:  "quiet-teal-otter",
				Text:    "revisit once the customer confirms",
				Trigger: "when the customer says the migration is done",
			},
		},
		OtherTasks: []OtherTask{
			{HarpID: "bold-gray-wren", Text: "ship the signing CLI", Status: "Done"},
		},
		Repo: RepoState{
			Dirs:           []string{"internal/signing", "internal/shared/tasks/triggers"},
			WorkingChanges: []string{"?? internal/shared/tasks/triggers/parse.go"},
		},
	}
}

// A whole CLASS of trigger is "when <thing> exists / has landed". Those are
// unanswerable from commit history alone (the thing may exist uncommitted, or
// have been committed before the task was deferred), so the repo's CURRENT
// state must reach the prompt.
func TestBuildPrompt_IncludesRepoState(t *testing.T) {
	p := BuildPrompt(sampleBatch())
	assert.Contains(t, p, "internal/shared/tasks/triggers", "the directory inventory answers existence-style triggers")
	assert.Contains(t, p, "?? internal/shared/tasks/triggers/parse.go", "uncommitted work is part of 'what exists now'")
}

// Absence of evidence is not evidence of absence. Without this rule the model
// reads a silent evidence pack as proof the condition did NOT occur and
// returns a confident not-fired — which parks a fired task forever, the exact
// mirror of the false-revive cannot-determine exists to prevent.
func TestBuildPrompt_ForbidsInferringAbsenceFromSilence(t *testing.T) {
	p := BuildPrompt(sampleBatch())
	assert.Contains(t, p, "Absence of evidence is not evidence of absence")
	// not-fired must be reserved for POSITIVE evidence the thing hasn't happened...
	assert.Contains(t, p, "POSITIVE reason to believe the condition has NOT occurred")
	// ...and a silent evidence pack must route to the two humble outcomes.
	assert.Contains(t, p, "NEVER \"not-fired\"")
}

func TestBuildPrompt_IncludesEveryTaskField(t *testing.T) {
	p := BuildPrompt(sampleBatch())

	assert.Contains(t, p, "swift-amber-falcon")
	assert.Contains(t, p, "wire the signing CLI")
	assert.Contains(t, p, "when the signing CLI ships")
	assert.Contains(t, p, "2026-06-01")
	assert.Contains(t, p, "feat(signing): ship the CLI")
	assert.Contains(t, p, "internal/signing/cli.go")

	assert.Contains(t, p, "quiet-teal-otter")
	assert.Contains(t, p, "revisit once the customer confirms")
	assert.Contains(t, p, "when the customer says the migration is done")
	assert.Contains(t, p, "unknown", "a task with no known deferred-at time says so rather than printing a zero-value time")
}

func TestBuildPrompt_IncludesOtherTasks(t *testing.T) {
	p := BuildPrompt(sampleBatch())
	assert.Contains(t, p, "bold-gray-wren")
	assert.Contains(t, p, "ship the signing CLI")
	assert.Contains(t, p, "Done")
}

func TestBuildPrompt_ListsAllFourOutcomesWithCannotDetermineEscapeHatch(t *testing.T) {
	p := BuildPrompt(sampleBatch())
	for _, o := range Outcomes() {
		assert.Contains(t, p, string(o))
	}
	// The escape hatch must be explained, not just named — this is the whole
	// point of the outcome (see verdict.go doc comment): without an explicit
	// instruction to prefer it over guessing, a model under pressure to
	// classify will guess "fired".
	assert.Contains(t, p, "guessing")
}

func TestBuildPrompt_DemandsStrictJSONResponseShape(t *testing.T) {
	p := BuildPrompt(sampleBatch())
	assert.Contains(t, p, "harp_id")
	assert.Contains(t, p, "outcome")
	assert.Contains(t, p, "confidence")
	assert.Contains(t, p, "evidence")
	assert.Contains(t, p, "reasoning")
	assert.Contains(t, p, "JSON array")
}

func TestBuildPrompt_EmptyBatchDoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		p := BuildPrompt(Batch{})
		assert.NotEmpty(t, p)
	})
}

func TestBuildPrompt_NoCommitsSaysSo(t *testing.T) {
	b := Batch{Now: time.Now(), Tasks: []TaskInput{{HarpID: "a", Text: "t", Trigger: "x"}}}
	p := BuildPrompt(b)
	assert.Contains(t, p, "none gathered")
}

// The query protocol must spell out the whitelist explicitly in the prompt —
// it IS the contract restricting what the model may ask for.
func TestBuildPrompt_ExplainsTheQueryWhitelist(t *testing.T) {
	p := BuildPrompt(sampleBatch())
	for _, qt := range QueryTypes() {
		assert.Contains(t, p, string(qt))
	}
	assert.Contains(t, p, "no \"..\"")
	assert.Contains(t, p, "queries")
}

func sampleFollowupBatch() FollowupBatch {
	return FollowupBatch{
		Now: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
		Tasks: []FollowupTask{
			{
				TaskInput: TaskInput{
					HarpID:  "swift-amber-falcon",
					Text:    "wire the signing CLI",
					Trigger: "when the signing CLI ships",
				},
				Results: []QueryResult{
					{Query: Query{Type: QueryPathExists, Path: "internal/signing/cli.go"}, Output: "exists"},
					{Query: Query{Type: QueryGrep, Pattern: "func Sign"}, Err: "pattern did not compile"},
				},
			},
		},
	}
}

func TestBuildFollowupPrompt_IncludesQueryResults(t *testing.T) {
	p := BuildFollowupPrompt(sampleFollowupBatch())
	assert.Contains(t, p, "swift-amber-falcon")
	assert.Contains(t, p, "path_exists(internal/signing/cli.go)")
	assert.Contains(t, p, "exists")
	assert.Contains(t, p, "pattern did not compile")
}

// Round 2 must not offer needs-investigation as an outcome — that's the hard
// cap preventing an infinite escalation loop.
func TestBuildFollowupPrompt_NeverOffersNeedsInvestigation(t *testing.T) {
	p := BuildFollowupPrompt(sampleFollowupBatch())
	assert.NotContains(t, p, "needs-investigation\":")
	assert.Contains(t, p, "fired|not-fired|cannot-determine")
}

func TestBuildFollowupPrompt_EmptyBatchDoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		p := BuildFollowupPrompt(FollowupBatch{})
		assert.NotEmpty(t, p)
	})
}

func TestBuildFollowupPrompt_NoQueryResultsOmitsSection(t *testing.T) {
	b := FollowupBatch{Tasks: []FollowupTask{{TaskInput: TaskInput{HarpID: "a", Text: "t", Trigger: "x"}}}}
	p := BuildFollowupPrompt(b)
	assert.NotContains(t, p, "Query results requested")
}
