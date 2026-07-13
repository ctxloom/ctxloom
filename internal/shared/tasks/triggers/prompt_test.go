package triggers

import (
	"strings"
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

// Every optional section of the prompt is gated on actually having something to
// say. The tests above prove those gates OPEN; these prove they CLOSE, which is
// the half that was missing.
//
// This is not cosmetic. An empty section is not neutral — a "Directories present
// in the repository:" header with nothing beneath it reads to the model as
// positive evidence that the repository contains no directories, and positive
// evidence of absence is exactly what licenses a confident not-fired. The gates
// are a safety property, so their closed state is asserted.
func TestBuildPrompt_OmitsOptionalSectionsWhenThereIsNothingToSay(t *testing.T) {
	b := Batch{
		Now:   time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
		Tasks: []TaskInput{{HarpID: "a", Text: "t", Trigger: "x"}},
	}
	p := BuildPrompt(b)

	assert.NotContains(t, p, "=== Other tasks", "no other tasks were supplied to cross-reference")
	assert.NotContains(t, p, "=== Repository state right now ===", "no repo state was gathered")
	assert.NotContains(t, p, "Changed files:", "the task has no changed files")
}

func TestWriteRepoState_RendersOnlyTheHalvesThatHaveContent(t *testing.T) {
	cases := []struct {
		name        string
		repo        RepoState
		wantSection bool
		wantDirs    bool
		wantChanges bool
	}{
		{name: "nothing gathered", repo: RepoState{}},
		{
			name:        "directories only",
			repo:        RepoState{Dirs: []string{"internal/signing"}},
			wantSection: true, wantDirs: true,
		},
		{
			name:        "working changes only",
			repo:        RepoState{WorkingChanges: []string{"?? internal/new.go"}},
			wantSection: true, wantChanges: true,
		},
		{
			name:        "both",
			repo:        RepoState{Dirs: []string{"internal/signing"}, WorkingChanges: []string{"?? internal/new.go"}},
			wantSection: true, wantDirs: true, wantChanges: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			writeRepoState(&sb, tc.repo)
			got := sb.String()

			if !tc.wantSection {
				assert.Empty(t, got, "an empty repo state must write NOTHING, not a bare header")
				return
			}
			assert.Contains(t, got, "=== Repository state right now ===")

			dirs := "Directories present in the repository:"
			changes := "Uncommitted working-tree changes"
			if tc.wantDirs {
				assert.Contains(t, got, dirs)
			} else {
				assert.NotContains(t, got, dirs)
			}
			if tc.wantChanges {
				assert.Contains(t, got, changes)
			} else {
				assert.NotContains(t, got, changes)
			}
		})
	}
}

// describeQuery is how the model sees its own round-1 request echoed back in
// round 2. A label that misnames the query it answers silently attributes
// evidence to the wrong question.
func TestDescribeQuery(t *testing.T) {
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"path_exists", Query{Type: QueryPathExists, Path: "internal/foo.go"}, "path_exists(internal/foo.go)"},
		{"grep without a glob", Query{Type: QueryGrep, Pattern: "func Sign"}, `grep("func Sign")`},
		{"grep with a glob", Query{Type: QueryGrep, Pattern: "func Sign", PathGlob: "internal"}, `grep("func Sign", internal)`},
		{"git_log_path", Query{Type: QueryGitLogPath, Path: "internal/signing"}, "git_log_path(internal/signing)"},
		{"task_status", Query{Type: QueryTaskStatus, HarpID: "bold-gray-wren"}, "task_status(bold-gray-wren)"},
		{"unknown type falls back to the raw type", Query{Type: "shell_exec"}, "shell_exec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describeQuery(tc.q))
		})
	}
}

// Round 2's whole purpose is to show the model what its round-1 requests
// produced. A query that errored must not swallow the results after it, and a
// query that found nothing must SAY it found nothing — a line that renders blank
// is indistinguishable from a query that was never run.
func TestWriteQueryResults_EveryRequestIsAccountedFor(t *testing.T) {
	var sb strings.Builder
	writeQueryResults(&sb, []QueryResult{
		{Query: Query{Type: QueryGrep, Pattern: "func Sign"}, Err: "pattern did not compile"},
		{Query: Query{Type: QueryPathExists, Path: "internal/foo.go"}, Output: "exists"},
		{Query: Query{Type: QueryPathExists, Path: "internal/bar.go"}},
	})
	got := sb.String()

	assert.Contains(t, got, `grep("func Sign"): ERROR: pattern did not compile`)
	assert.Contains(t, got, "path_exists(internal/foo.go): exists",
		"a result AFTER an errored one must still be rendered")
	assert.Contains(t, got, "path_exists(internal/bar.go): (no matches)",
		"an empty result must say so — a blank line reads as a query that never ran")
}

func TestShortSHA(t *testing.T) {
	assert.Equal(t, "abcdef1234", shortSHA("abcdef1234567890"), "a full hash is trimmed to a recognizable prefix")
	assert.Equal(t, "abcdef1234", shortSHA("abcdef1234"), "a hash already at the limit is returned whole")
	assert.Equal(t, "abc", shortSHA("abc"), "a short hash is returned as-is, never sliced past its end")
	assert.Equal(t, "", shortSHA(""))
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
