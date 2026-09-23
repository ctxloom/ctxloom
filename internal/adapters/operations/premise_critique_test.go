package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const critiqueYAML = `findings:
  - kind: too-broad
    evidence: "fires on every git command"
    counterexample: "running git status"
    patch: "You are about to delete a worktree directory."
  - kind: overlaps
    evidence: "same moment as the worktree-lifecycle fragment"
    overlaps_with: "ops#fragments/worktree-lifecycle"
`

var critiqueDraft = &PremiseDraft{
	Fragment: "ops#fragments/worktree-isolation",
	Premise:  "You are using git.",
	Moments:  []string{"removing a worktree"},
}

var critiqueSiblings = []PremiseIndexEntry{
	{Name: "ops#fragments/worktree-lifecycle", Premise: "You are about to create or remove a worktree."},
}

func TestCritiquePremise_ParsesFindingsAndSendsBodyDraftAndSiblings(t *testing.T) {
	run, captured := draftRunner(critiqueYAML)

	got, err := CritiquePremise(context.Background(), PremiseAuthorConfig{Run: run},
		"ops#fragments/worktree-isolation", "Never remove a worktree you did not create.", critiqueDraft, critiqueSiblings)
	require.NoError(t, err)

	assert.Equal(t, "ops#fragments/worktree-isolation", got.Fragment)
	require.Len(t, got.Findings, 2)
	assert.Equal(t, PremiseFinding{
		Kind:           FindingTooBroad,
		Evidence:       "fires on every git command",
		Counterexample: "running git status",
		Patch:          "You are about to delete a worktree directory.",
	}, got.Findings[0])
	assert.Equal(t, FindingOverlaps, got.Findings[1].Kind)
	assert.Equal(t, "ops#fragments/worktree-lifecycle", got.Findings[1].OverlapsWith)

	// What actually reached the LLM: the critique prompt, the FULL body, the
	// draft under attack, and the sibling premises it could collide with.
	assert.Contains(t, captured.Prompt, premiseCritiquePromptEmbeddedMarker(t))
	assert.Contains(t, captured.Prompt, `<fragment name="ops#fragments/worktree-isolation">`)
	assert.Contains(t, captured.Prompt, "Never remove a worktree you did not create.")
	assert.Contains(t, captured.Prompt, "You are using git.")
	assert.Contains(t, captured.Prompt, "removing a worktree")
	assert.Contains(t, captured.Prompt, "ops#fragments/worktree-lifecycle")
	assert.Contains(t, captured.Prompt, "You are about to create or remove a worktree.")
}

// premiseCritiquePromptEmbeddedMarker is the embedded prompt's first line —
// whatever the human writes there, the payload must be preceded by it.
func premiseCritiquePromptEmbeddedMarker(t *testing.T) string {
	t.Helper()
	p, err := premisePrompt("", premiseCritiquePromptName)
	require.NoError(t, err)
	for i, c := range p {
		if c == '\n' {
			return p[:i]
		}
	}
	return p
}

func TestCritiquePremise_AlwaysLoadDraftIsSentAsNONE(t *testing.T) {
	run, captured := draftRunner("findings: []")
	got, err := CritiquePremise(context.Background(), PremiseAuthorConfig{Run: run},
		"voice", "House voice.", &PremiseDraft{Fragment: "voice"}, nil)
	require.NoError(t, err)
	assert.Empty(t, got.Findings, "an empty findings list is a clean critique, not an error")
	assert.Contains(t, captured.Prompt, "premise: NONE",
		"an always-load draft must reach the critic as the NONE verdict, not as a missing premise")
}

func TestCritiquePremise_EveryKindParses(t *testing.T) {
	for _, kind := range []PremiseFindingKind{
		FindingTooBroad, FindingTooNarrow, FindingNamesMode, FindingUnobservable,
		FindingOverlaps, FindingLeaksBody, FindingSplitMissed, FindingShouldBeNone,
	} {
		t.Run(string(kind), func(t *testing.T) {
			run, _ := draftRunner("findings:\n  - kind: " + string(kind) + "\n    evidence: e\n")
			got, err := CritiquePremise(context.Background(), PremiseAuthorConfig{Run: run}, "f", "body", critiqueDraft, nil)
			require.NoError(t, err)
			require.Len(t, got.Findings, 1)
			assert.Equal(t, kind, got.Findings[0].Kind)
		})
	}
}

func TestCritiquePremise_RejectsMalformedOutput(t *testing.T) {
	for name, tc := range map[string]struct{ out, wantErr string }{
		"prose":         {"The premise looks fine to me.", "not the instructed YAML shape"},
		"no findings":   {"premise: something\n", "no findings field"},
		"unknown kind":  {"findings:\n  - kind: too-vague\n    evidence: e\n", `unknown finding kind "too-vague"`},
		"missing kind":  {"findings:\n  - evidence: e\n", `unknown finding kind ""`},
		"findings type": {"findings: nope\n", "not the instructed YAML shape"},
	} {
		t.Run(name, func(t *testing.T) {
			run, _ := draftRunner(tc.out)
			_, err := CritiquePremise(context.Background(), PremiseAuthorConfig{Run: run}, "f", "body", critiqueDraft, nil)
			require.Error(t, err, "malformed critic output must refuse, never yield a guessed critique")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestCritiquePremise_RefusesBeforeReachingTheLLM(t *testing.T) {
	for name, call := range map[string]func(PremiseAuthorConfig) error{
		"empty name": func(c PremiseAuthorConfig) error {
			_, err := CritiquePremise(context.Background(), c, " ", "body", critiqueDraft, nil)
			return err
		},
		"empty body": func(c PremiseAuthorConfig) error {
			_, err := CritiquePremise(context.Background(), c, "f", "\n", critiqueDraft, nil)
			return err
		},
		"nil draft": func(c PremiseAuthorConfig) error {
			_, err := CritiquePremise(context.Background(), c, "f", "body", nil, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			run, captured := draftRunner(critiqueYAML)
			require.Error(t, call(PremiseAuthorConfig{Run: run}))
			assert.Empty(t, captured.Prompt, "a refused critique must not reach the LLM")
		})
	}
}

func TestCritiquePremise_LLMFailurePropagates(t *testing.T) {
	run := func(context.Context, string) (string, error) { return "", errors.New("connection failed") }
	_, err := CritiquePremise(context.Background(), PremiseAuthorConfig{Run: run}, "f", "body", critiqueDraft, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")
}

func TestCritiquePremise_PromptDirOverridesAndHardFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "premise-critique.md"), []byte("VARIANT CRITIC"), 0o644))

	run, captured := draftRunner(critiqueYAML)
	_, err := CritiquePremise(context.Background(), PremiseAuthorConfig{Run: run, PromptDir: dir}, "f", "body", critiqueDraft, nil)
	require.NoError(t, err)
	assert.Contains(t, captured.Prompt, "VARIANT CRITIC")
	assert.NotContains(t, captured.Prompt, premiseCritiquePromptEmbeddedMarker(t))

	// The directory holds the critique prompt but not the author one: the
	// lookup is keyed by prompt name, so neither pass can pick up the other's.
	run2, captured2 := draftRunner(draftYAML)
	_, err = DraftPremise(context.Background(), PremiseAuthorConfig{Run: run2, PromptDir: dir}, "f", "body")
	require.Error(t, err)
	assert.Empty(t, captured2.Prompt)
}
