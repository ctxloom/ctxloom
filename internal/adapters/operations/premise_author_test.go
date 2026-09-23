package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedRun holds the request DraftPremise actually sent — the assertions
// that matter here are against what reached the LLM, not what the CLI-side
// code says it sent. Req stays nil until a call reaches the client.
type capturedRun struct{ Prompt string }

// draftRunner returns a runner that answers out, plus the capture slot for
// the prompt it received.
func draftRunner(out string) (memory.Runner, *capturedRun) {
	captured := &capturedRun{}
	return func(_ context.Context, prompt string) (string, error) {
		captured.Prompt = prompt
		return out, nil
	}, captured
}

const draftYAML = `premise: "You are about to declare an error value, or assert on one."
moments:
  - "writing a sentinel error"
  - "asserting an error message in a test"
not_for:
  - "branching on a non-error string"
split: ""
`

func TestDraftPremise_ParsesTheModelDocument(t *testing.T) {
	run, captured := draftRunner(draftYAML)

	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{Run: run},
		"error-constants", "Use sentinel errors, never string matching.")
	require.NoError(t, err)

	assert.Equal(t, "error-constants", draft.Fragment)
	assert.Equal(t, "You are about to declare an error value, or assert on one.", draft.Premise)
	assert.Equal(t, []string{"writing a sentinel error", "asserting an error message in a test"}, draft.Moments)
	assert.Equal(t, []string{"branching on a non-error string"}, draft.NotFor)
	assert.Empty(t, draft.SplitHint)

	// What actually reached the LLM: the authoring prompt, then the fragment
	// enveloped with its name.
	assert.Contains(t, captured.Prompt, "drafting a PREMISE",
		"the authoring prompt must precede the fragment")
	assert.Contains(t, captured.Prompt, `<fragment name="error-constants">`)
	assert.Contains(t, captured.Prompt, "Use sentinel errors, never string matching.")
}

func TestDraftPremise_SplitHintSurvives(t *testing.T) {
	run, _ := draftRunner(`premise: "You are testing strings, or, unrelatedly, declaring errors."
split: "The flow-control half fires at a conditional on a string; the error half fires when declaring an error. Split them."
`)
	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{Run: run}, "strings", "two unrelated jobs")
	require.NoError(t, err)
	assert.Equal(t,
		"The flow-control half fires at a conditional on a string; the error half fires when declaring an error. Split them.",
		draft.SplitHint)
}

func TestDraftPremise_NoneMeansAlwaysLoad(t *testing.T) {
	run, _ := draftRunner(`premise: NONE
moments:
  - "a house voice applies to every turn"
`)
	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{Run: run}, "voice", "House writing voice.")
	require.NoError(t, err)
	assert.Empty(t, draft.Premise, "NONE is the always-load verdict: an empty Premise, exactly what an unpremised fragment means")
	assert.Equal(t, []string{"a house voice applies to every turn"}, draft.Moments)
}

func TestDraftPremise_ToleratesACodeFence(t *testing.T) {
	run, _ := draftRunner("```yaml\n" + draftYAML + "```")
	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{Run: run}, "error-constants", "body")
	require.NoError(t, err)
	assert.Equal(t, "You are about to declare an error value, or assert on one.", draft.Premise)
}

func TestDraftPremise_RejectsOutputWithoutAPremise(t *testing.T) {
	// wantErr pins WHICH refusal fires: a missing premise field and an
	// explicitly empty one carry different remedies, and a mutation collapsing
	// one guard into the other survives a bare assert.Error.
	for name, tc := range map[string]struct{ out, wantErr string }{
		"prose":          {"I think this fragment is about error handling.", "not the instructed YAML shape"},
		"missing key":    {"moments:\n  - \"something\"\n", "no premise field"},
		"empty premise":  {`premise: ""`, "premise is empty"},
		"empty document": {"{}", "no premise field"},
	} {
		t.Run(name, func(t *testing.T) {
			run, _ := draftRunner(tc.out)
			_, err := DraftPremise(context.Background(),
				PremiseAuthorConfig{Run: run}, "frag", "body")
			require.Error(t, err, "malformed model output must refuse, never yield a guessed draft")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestDraftPremise_RefusesEmptyNameAndBody(t *testing.T) {
	run, captured := draftRunner(draftYAML)

	_, err := DraftPremise(context.Background(), PremiseAuthorConfig{Run: run}, "", "body")
	assert.Error(t, err)
	_, err = DraftPremise(context.Background(), PremiseAuthorConfig{Run: run}, "frag", "  \n")
	assert.Error(t, err)
	assert.Empty(t, captured.Prompt, "a refused draft must not have reached the LLM at all")
}

func TestDraftPremise_LLMFailurePropagates(t *testing.T) {
	run := func(context.Context, string) (string, error) { return "", errors.New("connection failed") }
	_, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{Run: run}, "frag", "body")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")
}

func TestDraftPremise_PromptDirOverridesAndHardFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "premise-author.md"), []byte("VARIANT PROMPT UNDER TEST"), 0o644))

	run, captured := draftRunner(draftYAML)
	_, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{Run: run, PromptDir: dir}, "frag", "body")
	require.NoError(t, err)
	assert.Contains(t, captured.Prompt, "VARIANT PROMPT UNDER TEST")
	assert.NotContains(t, captured.Prompt, "drafting a PREMISE",
		"the override must REPLACE the embedded prompt, not join it")

	// A directory without the named prompt is a hard failure, never a silent
	// fall back to the embedded text: the run must be attributable to the
	// prompt that produced it.
	run2, captured2 := draftRunner(draftYAML)
	_, err = DraftPremise(context.Background(),
		PremiseAuthorConfig{Run: run2, PromptDir: t.TempDir()}, "frag", "body")
	require.Error(t, err)
	assert.Empty(t, captured2.Prompt, "the hard failure must happen before any LLM call")
}

func TestDraftPremise_NotesAreOptionalAndParsed(t *testing.T) {
	run, _ := draftRunner(draftYAML + "notes: |\n  Exists because string-matched errors broke twice.\n")
	draft, err := DraftPremise(context.Background(), PremiseAuthorConfig{Run: run}, "error-constants", "body")
	require.NoError(t, err)
	assert.Equal(t, "Exists because string-matched errors broke twice.", draft.Notes)

	run, _ = draftRunner(draftYAML)
	draft, err = DraftPremise(context.Background(), PremiseAuthorConfig{Run: run}, "error-constants", "body")
	require.NoError(t, err)
	assert.Empty(t, draft.Notes, "notes are optional: a draft without them still parses")
}
