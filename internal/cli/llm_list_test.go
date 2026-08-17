// `llm list --format json` feeds frontends (the VSCode companion's model
// picker) the labels `-l`/`--llm` accepts, so the entry construction — label
// order preserved, exactly the primary label marked default — must hold.
package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLLMListEntries_MarksDefaultLabel(t *testing.T) {
	entries := llmListEntries([]string{"antigravity", "claude-code", "codex"}, "claude-code", nil, nil)

	assert.Equal(t, []llmEntry{
		{Label: "antigravity"},
		{Label: "claude-code", Default: true},
		{Label: "codex"},
	}, entries)
}

func TestLLMListEntries_NoDefaultWhenLabelEmpty(t *testing.T) {
	// Config-unavailable degradation: names enumerate, nothing is default.
	entries := llmListEntries([]string{"claude-code", "codex"}, "", nil, nil)

	for _, e := range entries {
		assert.False(t, e.Default, "no entry should be default without a primary label")
	}
}

// TestLLMListEntries_MarksAuthoredFromPredicate pins that the authored flag is
// carried per label, and that the degraded (nil predicate) path authors
// nothing rather than defaulting to "everything is yours" — the direction of
// that default is the whole safety property.
func TestLLMListEntries_MarksAuthoredFromPredicate(t *testing.T) {
	authored := func(name string) bool { return name == "mine" }

	assert.Equal(t, []llmEntry{
		{Label: "codex"},
		{Label: "mine", Default: true, Authored: true},
	}, llmListEntries([]string{"codex", "mine"}, "mine", authored, nil))

	for _, e := range llmListEntries([]string{"codex", "mine"}, "mine", nil, nil) {
		assert.False(t, e.Authored, "%s: a nil predicate authors nothing", e.Label)
	}
}

// --- authored vs registry-fallback -------------------------------------------
//
// `llm list` prints the UNION of the registered backends and the labels
// config.yaml declares, and used to render both identically — so a reader
// could not tell the five labels this project authored from the six bare
// engine names mergeDefaultConfig's whole-registry fallback supplied. That is
// the same conflation that let `llm remove claude-code` report success and
// delete nothing on a project that never wrote an llm.configs line.
//
// Both of these drive the real RunE against a real config.yaml, because the
// distinction is only meaningful once config.Config's lmDefaultOverlay is in
// play — a pure entry-construction test cannot tell the two apart.

func TestRunLLMList_TextMarksAuthoredAndFallbackDifferently(t *testing.T) {
	// One authored label ("big") and one name only the registry supplies
	// ("claude-code"), in a single listing. The default is pinned to a THIRD
	// name so the two markers are checked both with and without "(default)"
	// sitting between them.
	agentProject(t, "version: 6\nllm:\n  configs:\n    big: { type: codex }\n  defaults:\n    primary: codex\n")
	cmd, out := textCmd()
	require.NoError(t, runLLMList(cmd, nil))

	got := out.String()
	assert.Contains(t, got, "big [configured]",
		"a label config.yaml declares must be marked as the user's own")
	assert.Contains(t, got, "claude-code [built-in]",
		"a bare engine name the registry fallback supplied must be marked as such")
	assert.Contains(t, got, "codex (default) [built-in]",
		"the default marker keeps its place immediately after the label")
}

func TestRunLLMList_JSONCarriesAuthored(t *testing.T) {
	agentProject(t, "version: 6\nllm:\n  configs:\n    big: { type: codex }\n")
	cmd, out := textCmd()
	cmd.Flags().String("format", formatText, "")
	require.NoError(t, cmd.Flags().Set("format", formatJSON))
	require.NoError(t, runLLMList(cmd, nil))

	// Decoded as raw maps on purpose: this pins the WIRE contract the VSCode
	// companion reads (the "authored" key itself), not just the Go field.
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), "output: %s", out.String())

	authored := map[string]any{}
	for _, r := range rows {
		authored[r["label"].(string)] = r["authored"]
	}
	require.Contains(t, authored, "big")
	require.Contains(t, authored, "claude-code")
	assert.Equal(t, true, authored["big"], "the declared label is authored")
	assert.Equal(t, false, authored["claude-code"], "the registry-fallback name is not authored")
}

// TestRunLLMList_WholeRegistryFallbackIsNeverAuthored is the case the
// distinction exists for: a project with NO llm block at all gets every
// default label merged into its read view, and not one of them is the user's.
func TestRunLLMList_WholeRegistryFallbackIsNeverAuthored(t *testing.T) {
	agentProject(t, "version: 6\n")
	cmd, out := textCmd()
	cmd.Flags().String("format", formatText, "")
	require.NoError(t, cmd.Flags().Set("format", formatJSON))
	require.NoError(t, runLLMList(cmd, nil))

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), "output: %s", out.String())
	require.NotEmpty(t, rows)
	for _, r := range rows {
		assert.Equal(t, false, r["authored"],
			"%v: a project that declared no llm block authored nothing", r["label"])
	}
}

// A run whose config could not be loaded marks NOTHING as authored.
//
// This pins the direction of the degraded default, which is a claim and not a
// detail: answering the other way would stamp every one of ctxloom's own
// built-in engine names as the user's own configuration — precisely the
// misreading this listing was added to prevent, and worst exactly when the
// config is broken and the reader is already trying to work out what is real.
func TestLLMList_DegradedConfigAuthorsNothing(t *testing.T) {
	entries := llmListEntries([]string{"claude-code", "codex"}, "", noneAuthored, nil)

	require.Len(t, entries, 2, "the built-in names still list")
	for _, e := range entries {
		assert.False(t, e.Authored,
			"%s: with no config to have declared it, nothing is the user's own", e.Label)
	}
}

// --- the runtime offer -------------------------------------------------------
//
// `llm list` is the surface the agent-creating setup interview enumerates
// engines through, and the runtime question that interview must ask per agent
// is answerable only per ENGINE: not every engine can authenticate inside a
// container, and `agent create --runtime container-rootless` refuses the ones
// that cannot. So the listing carries the offer, in both formats, with the
// reason attached whenever the container axes are absent.

// TestRunLLMList_TextWithholdsContainerForEngineWithoutAuthAndSaysWhy is the
// gate seen from the CLI: a label bound to the generic "acp" backend (no
// container auth) renders host and nothing else, WITH the reason — a shorter
// menu on its own would read as ctxloom having an opinion about containers.
func TestRunLLMList_TextWithholdsContainerForEngineWithoutAuthAndSaysWhy(t *testing.T) {
	agentProject(t, "version: 6\nllm:\n  configs:\n    editor: { type: acp }\n")
	cmd, out := textCmd()
	require.NoError(t, runLLMList(cmd, nil))

	got := out.String()
	require.Contains(t, got, "editor (default) [configured]")
	assert.Regexp(t, "editor \\(default\\) \\[configured\\]\n\\s+runtimes: host\n",
		got, "an engine with no container auth is offered host and nothing else")
	assert.Regexp(t, `no container runtime: .*container auth`, got,
		"and the listing says why, rather than quietly presenting a shorter menu")
}

// TestRunLLMList_TextOffersBothContainerAxesForAnAuthedEngine proves the gate
// is not simply off for everyone.
func TestRunLLMList_TextOffersBothContainerAxesForAnAuthedEngine(t *testing.T) {
	agentProject(t, "version: 6\n")
	cmd, out := textCmd()
	require.NoError(t, runLLMList(cmd, nil))

	assert.Contains(t, out.String(), "runtimes: host, container-rootless, container-rootful",
		"an engine that can authenticate inside a container is offered both container axes")
}

// TestRunLLMList_JSONCarriesTheRuntimeOffer pins the WIRE keys, not just the Go
// fields: this is what a frontend (and the setup interview reading
// `--format json`) consults instead of carrying its own list of "engines that
// support containers".
func TestRunLLMList_JSONCarriesTheRuntimeOffer(t *testing.T) {
	agentProject(t, "version: 6\nllm:\n  configs:\n    editor: { type: acp }\n")
	cmd, out := textCmd()
	cmd.Flags().String("format", formatText, "")
	require.NoError(t, cmd.Flags().Set("format", formatJSON))
	require.NoError(t, runLLMList(cmd, nil))

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), "output: %s", out.String())

	byLabel := map[string]map[string]any{}
	for _, r := range rows {
		byLabel[r["label"].(string)] = r
	}
	require.Contains(t, byLabel, "editor")
	require.Contains(t, byLabel, "claude-code")

	assert.Equal(t, []any{"host"}, byLabel["editor"]["runtimes"],
		"the engine with no container auth is offered host alone")
	assert.NotEmpty(t, byLabel["editor"]["container_withheld"],
		"and the wire carries the reason too")
	assert.Equal(t, []any{"host", "container-rootless", "container-rootful"},
		byLabel["claude-code"]["runtimes"])
	assert.NotContains(t, byLabel["claude-code"], "container_withheld",
		"nothing is withheld for an authed engine, so the key is absent rather than empty")
}

// TestLLMListEntries_NilOfferLeavesRuntimesEmpty pins the degraded direction:
// with no config there is no label→backend resolution, so there is no offer to
// make — and the listing says nothing rather than guessing an axis a later
// write might refuse.
func TestLLMListEntries_NilOfferLeavesRuntimesEmpty(t *testing.T) {
	for _, e := range llmListEntries([]string{"claude-code", "acp"}, "", nil, nil) {
		assert.Empty(t, e.Runtimes, "%s: no config, no offer", e.Label)
		assert.Empty(t, e.ContainerWithheld, "%s", e.Label)
		assert.Empty(t, llmRuntimeLine(e), "%s: and nothing is rendered for it", e.Label)
	}
}
