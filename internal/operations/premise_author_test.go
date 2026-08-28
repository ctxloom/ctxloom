package operations

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// capturedRun holds the request DraftPremise actually sent — the assertions
// that matter here are against what reached the LLM, not what the CLI-side
// code says it sent. Req stays nil until a call reaches the client.
type capturedRun struct{ Req *pb.RunStart }

// draftClient returns a mock factory whose client emits out on stdout, plus
// the capture slot for the request it received.
func draftClient(out string) (pb.ClientFactory, *capturedRun) {
	captured := &capturedRun{}
	client := &pb.MockClient{
		RunFunc: func(_ context.Context, req *pb.RunStart, stdout, _ io.Writer) (int32, error) {
			captured.Req = req
			_, _ = stdout.Write([]byte(out))
			return 0, nil
		},
	}
	return pb.MockClientFactory(client), captured
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
	factory, captured := draftClient(draftYAML)

	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{ClientFactory: factory, Model: "haiku", Env: map[string]string{"K": "v"}},
		"error-constants", "Use sentinel errors, never string matching.")
	require.NoError(t, err)

	assert.Equal(t, "error-constants", draft.Fragment)
	assert.Equal(t, "You are about to declare an error value, or assert on one.", draft.Premise)
	assert.Equal(t, []string{"writing a sentinel error", "asserting an error message in a test"}, draft.Moments)
	assert.Equal(t, []string{"branching on a non-error string"}, draft.NotFor)
	assert.Empty(t, draft.SplitHint)

	// What actually reached the LLM: the authoring prompt, then the fragment
	// enveloped with its name — and the caller's model and env forwarded.
	require.NotNil(t, captured.Req)
	require.NotNil(t, captured.Req.Prompt)
	assert.Contains(t, captured.Req.Prompt.Content, "drafting a PREMISE",
		"the authoring prompt must precede the fragment")
	assert.Contains(t, captured.Req.Prompt.Content, `<fragment name="error-constants">`)
	assert.Contains(t, captured.Req.Prompt.Content, "Use sentinel errors, never string matching.")
	require.NotNil(t, captured.Req.Options)
	assert.Equal(t, "haiku", captured.Req.Options.Model)
	assert.Equal(t, map[string]string{"K": "v"}, captured.Req.Options.Env)
}

func TestDraftPremise_SplitHintSurvives(t *testing.T) {
	factory, _ := draftClient(`premise: "You are testing strings, or, unrelatedly, declaring errors."
split: "The flow-control half fires at a conditional on a string; the error half fires when declaring an error. Split them."
`)
	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{ClientFactory: factory}, "strings", "two unrelated jobs")
	require.NoError(t, err)
	assert.Equal(t,
		"The flow-control half fires at a conditional on a string; the error half fires when declaring an error. Split them.",
		draft.SplitHint)
}

func TestDraftPremise_NoneMeansAlwaysLoad(t *testing.T) {
	factory, _ := draftClient(`premise: NONE
moments:
  - "a house voice applies to every turn"
`)
	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{ClientFactory: factory}, "voice", "House writing voice.")
	require.NoError(t, err)
	assert.Empty(t, draft.Premise, "NONE is the always-load verdict: an empty Premise, exactly what an unpremised fragment means")
	assert.Equal(t, []string{"a house voice applies to every turn"}, draft.Moments)
}

func TestDraftPremise_ToleratesACodeFence(t *testing.T) {
	factory, _ := draftClient("```yaml\n" + draftYAML + "```")
	draft, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{ClientFactory: factory}, "error-constants", "body")
	require.NoError(t, err)
	assert.Equal(t, "You are about to declare an error value, or assert on one.", draft.Premise)
}

func TestDraftPremise_RejectsOutputWithoutAPremise(t *testing.T) {
	for name, out := range map[string]string{
		"prose":          "I think this fragment is about error handling.",
		"missing key":    "moments:\n  - \"something\"\n",
		"empty premise":  `premise: ""`,
		"empty document": "{}",
	} {
		t.Run(name, func(t *testing.T) {
			factory, _ := draftClient(out)
			_, err := DraftPremise(context.Background(),
				PremiseAuthorConfig{ClientFactory: factory}, "frag", "body")
			assert.Error(t, err, "malformed model output must refuse, never yield a guessed draft")
		})
	}
}

func TestDraftPremise_RefusesEmptyNameAndBody(t *testing.T) {
	factory, captured := draftClient(draftYAML)

	_, err := DraftPremise(context.Background(), PremiseAuthorConfig{ClientFactory: factory}, "", "body")
	assert.Error(t, err)
	_, err = DraftPremise(context.Background(), PremiseAuthorConfig{ClientFactory: factory}, "frag", "  \n")
	assert.Error(t, err)
	assert.Nil(t, captured.Req, "a refused draft must not have reached the LLM at all")
}

func TestDraftPremise_LLMFailurePropagates(t *testing.T) {
	client := &pb.MockClient{
		RunFunc: func(_ context.Context, _ *pb.RunStart, _, _ io.Writer) (int32, error) {
			return 0, errors.New("connection failed")
		},
	}
	_, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{ClientFactory: pb.MockClientFactory(client)}, "frag", "body")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")
}

func TestDraftPremise_PromptDirOverridesAndHardFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "premise-author.md"), []byte("VARIANT PROMPT UNDER TEST"), 0o644))

	factory, captured := draftClient(draftYAML)
	_, err := DraftPremise(context.Background(),
		PremiseAuthorConfig{ClientFactory: factory, PromptDir: dir}, "frag", "body")
	require.NoError(t, err)
	assert.Contains(t, captured.Req.Prompt.Content, "VARIANT PROMPT UNDER TEST")
	assert.NotContains(t, captured.Req.Prompt.Content, "drafting a PREMISE",
		"the override must REPLACE the embedded prompt, not join it")

	// A directory without the named prompt is a hard failure, never a silent
	// fall back to the embedded text: the run must be attributable to the
	// prompt that produced it.
	factory2, captured2 := draftClient(draftYAML)
	_, err = DraftPremise(context.Background(),
		PremiseAuthorConfig{ClientFactory: factory2, PromptDir: t.TempDir()}, "frag", "body")
	require.Error(t, err)
	assert.Nil(t, captured2.Req, "the hard failure must happen before any LLM call")
}
