package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

const (
	stubDraftYAML = `premise: "You are about to delete a worktree."
moments: ["removing a worktree directory"]
split: "The branch half fires at a different moment. Split it."
notes: "Drafted origin."
`
	stubCritiqueYAML = `findings:
  - kind: too-broad
    evidence: "fires on every worktree command"
    patch: "You are about to delete a worktree you did not create."
`
)

// premiseSessions records every session the command started, in order, with
// the prompts each one received — so a test can see that the critique ran on
// a session of its own.
type premiseSessions struct {
	labels  []string
	prompts [][]string
	ended   int
}

// stubPremiseRunners replaces newPremiseRunner: the first session answers as
// the drafter, the second as the critic.
func stubPremiseRunners(t *testing.T) *premiseSessions {
	t.Helper()
	s := &premiseSessions{}
	orig := newPremiseRunner
	t.Cleanup(func() { newPremiseRunner = orig })
	newPremiseRunner = func(_ context.Context, _ *config.Config, label string) (memory.Runner, func(), error) {
		i := len(s.labels)
		s.labels = append(s.labels, label)
		s.prompts = append(s.prompts, nil)
		answer := stubDraftYAML
		if i > 0 {
			answer = stubCritiqueYAML
		}
		return func(_ context.Context, prompt string) (string, error) {
			s.prompts[i] = append(s.prompts[i], prompt)
			return answer, nil
		}, func() { s.ended++ }, nil
	}
	return s
}

// seedDraftPremiseProject makes bundle "demo" with the fragment under
// authoring (x, notes but no premise) and a premised sibling (y).
func seedDraftPremiseProject(t *testing.T, existingNotes string) *config.Config {
	t.Helper()
	resetApp()
	t.Cleanup(resetApp)
	cfg := setupEditProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: "demo",
		Fragments: map[string]operations.BundleFragmentInput{
			"x": {Content: "Never remove a worktree you did not create.", Notes: existingNotes, NoDistill: true},
			"y": {Content: "sibling body", Premise: "You are about to create a branch.", NoDistill: true},
		},
	})
	require.NoError(t, err)
	return cfg
}

func setDraftPremiseFlags(t *testing.T, noCritique, dryRun bool) {
	t.Helper()
	fragmentDraftPremiseLLM, fragmentDraftPremiseNoCritique, fragmentDraftPremiseDryRun = "mock", noCritique, dryRun
	t.Cleanup(func() {
		fragmentDraftPremiseLLM, fragmentDraftPremiseNoCritique, fragmentDraftPremiseDryRun, fragmentDraftPremisePromptDir = "", false, false, ""
	})
}

// atTerminal presents the human side of the split, answering the prompt with
// the given lines.
func atTerminal(t *testing.T, answers string) {
	t.Helper()
	origTTY, origIn := isInteractiveTerminal, stdinReader
	t.Cleanup(func() { isInteractiveTerminal, stdinReader = origTTY, origIn })
	isInteractiveTerminal = func() bool { return true }
	stdinReader = bufio.NewReader(strings.NewReader(answers))
}

func fragmentX(t *testing.T, cfg *config.Config) *operations.GetItemResult {
	t.Helper()
	got, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{Bundle: "demo", Kind: operations.ItemKindFragment, Name: "x"})
	require.NoError(t, err)
	return got
}

func TestDraftPremise_OffTerminalProposesAndWritesNothing(t *testing.T) {
	cfg := seedDraftPremiseProject(t, "")
	sessions := stubPremiseRunners(t)
	setDraftPremiseFlags(t, false, false)

	cmd, out := formatCmd("json")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))

	var got premiseProposal
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	assert.Equal(t, "demo#fragments/x", got.Ref)
	assert.Equal(t, premiseDecisionProposed, got.Decision)
	assert.Nil(t, got.Written)
	assert.Equal(t, "You are about to delete a worktree.", got.Draft.Premise)
	require.NotNil(t, got.Critique)
	require.Len(t, got.Critique.Findings, 1)
	assert.Equal(t, operations.FindingTooBroad, got.Critique.Findings[0].Kind)

	assert.Empty(t, fragmentX(t, cfg).Premise, "off a terminal nothing is ratified, so nothing is written")

	// Two passes, two sessions: the critic never shares the drafter's history.
	require.Len(t, sessions.labels, 2, "draft and critique must each start their own session")
	assert.Equal(t, []string{"mock", "mock"}, sessions.labels)
	assert.Equal(t, 2, sessions.ended)
	require.Len(t, sessions.prompts[1], 1)
	critic := sessions.prompts[1][0]
	assert.Contains(t, critic, "You are about to delete a worktree.", "the critic attacks the draft")
	assert.Contains(t, critic, "demo#fragments/y", "siblings reach the critic")
	assert.NotContains(t, critic, "- name: demo#fragments/x", "the fragment is not its own sibling")
}

func TestDraftPremise_TextProposalShowsSplitFirstAndFindings(t *testing.T) {
	seedDraftPremiseProject(t, "Existing origin.")
	stubPremiseRunners(t)
	setDraftPremiseFlags(t, false, false)

	cmd, out := formatCmd("text")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))
	text := out.String()

	split := strings.Index(text, "SPLIT SUGGESTED")
	draft := strings.Index(text, "Draft premise:")
	require.GreaterOrEqual(t, split, 0)
	assert.Less(t, split, draft, "the split hint leads, ahead of the draft")
	assert.Contains(t, text, "Current premise: NONE (always loads)")
	assert.Contains(t, text, "Existing origin.")
	assert.Contains(t, text, "[too-broad]")
	assert.Contains(t, text, "Nothing written")
}

func TestDraftPremise_DryRunAtTerminalWritesNothing(t *testing.T) {
	cfg := seedDraftPremiseProject(t, "")
	stubPremiseRunners(t)
	setDraftPremiseFlags(t, false, true)
	atTerminal(t, "a\n")

	cmd, _ := formatCmd("text")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))
	assert.Empty(t, fragmentX(t, cfg).Premise)
}

func TestDraftPremise_AcceptWritesPremiseKeepsNotesAndNeverSigns(t *testing.T) {
	cfg := seedDraftPremiseProject(t, "Existing origin.")
	stubPremiseRunners(t)
	setDraftPremiseFlags(t, false, false)
	atTerminal(t, "a\n")

	cmd, out := formatCmd("text")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))

	got := fragmentX(t, cfg)
	assert.Equal(t, "You are about to delete a worktree.", got.Premise)
	assert.Equal(t, "Existing origin.", got.Notes, "accepting never overwrites existing notes")
	assert.Contains(t, out.String(), "approvals are now stale")
	assert.Contains(t, out.String(), "ctxloom bundle sign demo")
}

func TestDraftPremise_AcceptOffersDraftNotesWhereThereAreNone(t *testing.T) {
	cfg := seedDraftPremiseProject(t, "")
	stubPremiseRunners(t)
	setDraftPremiseFlags(t, true, false)
	atTerminal(t, "a\n")

	cmd, _ := formatCmd("text")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))
	assert.Equal(t, "Drafted origin.", fragmentX(t, cfg).Notes)
}

func TestDraftPremise_NoCritiqueStartsOneSession(t *testing.T) {
	seedDraftPremiseProject(t, "")
	sessions := stubPremiseRunners(t)
	setDraftPremiseFlags(t, true, false)

	cmd, out := formatCmd("json")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))
	assert.Len(t, sessions.labels, 1)
	assert.NotContains(t, out.String(), `"critique"`)
}

func TestDraftPremise_RejectWritesNothing(t *testing.T) {
	cfg := seedDraftPremiseProject(t, "")
	stubPremiseRunners(t)
	setDraftPremiseFlags(t, false, false)
	atTerminal(t, "x\nr\n")

	cmd, out := formatCmd("text")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))
	assert.Empty(t, fragmentX(t, cfg).Premise)
	assert.Contains(t, out.String(), "Rejected: nothing written.")
}

func TestDraftPremise_EditNONEMeansAlwaysLoad(t *testing.T) {
	cfg := seedDraftPremiseProject(t, "")
	_, err := operations.SetFragmentPremise(context.Background(), cfg, operations.SetFragmentPremiseRequest{Bundle: "demo", Name: "x", Premise: "old premise"})
	require.NoError(t, err)
	stubPremiseRunners(t)
	setDraftPremiseFlags(t, false, false)
	atTerminal(t, "e\n")
	setFakeEditor(t, "# a comment line\npremise: NONE\nnotes: Edited origin.\n")

	cmd, out := formatCmd("text")
	cmd.SetContext(context.Background())
	require.NoError(t, runFragmentDraftPremise(cmd, []string{"demo#fragments/x"}))

	got := fragmentX(t, cfg)
	assert.Empty(t, got.Premise, "NONE is always-load")
	assert.Equal(t, "Edited origin.", got.Notes)
	assert.Contains(t, out.String(), "approvals are now stale")
}

func TestDraftPremise_EditedEmptyPremiseIsRefused(t *testing.T) {
	for name, saved := range map[string]string{
		"blank value": "premise: \"\"\nnotes: n\n",
		"key deleted": "notes: n\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := seedDraftPremiseProject(t, "")
			_, err := operations.SetFragmentPremise(context.Background(), cfg, operations.SetFragmentPremiseRequest{Bundle: "demo", Name: "x", Premise: "old premise"})
			require.NoError(t, err)
			stubPremiseRunners(t)
			setDraftPremiseFlags(t, false, false)
			atTerminal(t, "e\n")
			setFakeEditor(t, saved)

			cmd, _ := formatCmd("text")
			cmd.SetContext(context.Background())
			err = runFragmentDraftPremise(cmd, []string{"demo#fragments/x"})
			require.ErrorIs(t, err, errPremiseEditEmpty)
			assert.Equal(t, "old premise", fragmentX(t, cfg).Premise, "a refused edit writes nothing")
		})
	}
}

func TestPremiseEditHeader_CarriesCritiqueAsComments(t *testing.T) {
	p := &premiseProposal{
		Draft:    &operations.PremiseDraft{Premise: "p", SplitHint: "split it"},
		Critique: &operations.PremiseCritique{Findings: []operations.PremiseFinding{{Kind: operations.FindingOverlaps, Evidence: "e", OverlapsWith: "demo#fragments/y"}}},
	}
	header := premiseEditHeader(p)
	for _, line := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
		assert.True(t, strings.HasPrefix(line, "#"), "every header line must be a comment the parse drops: %q", line)
	}
	assert.Contains(t, header, "[overlaps]")
	assert.Contains(t, header, "overlaps with: demo#fragments/y")
	assert.Contains(t, header, "split it")
	assert.Contains(t, header, "NONE")

	got, err := parseEditedPremise(header + "premise: kept\n")
	require.NoError(t, err)
	assert.Equal(t, "kept", got.Premise)
}
