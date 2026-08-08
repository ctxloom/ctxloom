package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestParseSurfaceOverrides_ParsesPairsAndRejectsNamesThatExistNowhere(t *testing.T) {
	got, err := parseSurfaceOverrides([]string{"context=unsafe-file", "skills=hook"})
	require.NoError(t, err)
	assert.Equal(t, map[agent.SurfaceKind]agent.Approach{
		agent.SurfaceContext: agent.ApproachUnsafeFile,
		agent.SurfaceSkills:  agent.ApproachHook,
	}, got)

	// No pairs must yield NO map rather than an empty one: an empty map and a
	// nil map both mean "no overrides" to the caller, but only nil says it
	// without inviting a reader to wonder which kinds were cleared.
	none, err := parseSurfaceOverrides(nil)
	require.NoError(t, err)
	assert.Nil(t, none)

	for _, bad := range []string{"context", "ctxt=hook", "context=telepathy", "=hook", "context="} {
		_, err := parseSurfaceOverrides([]string{bad})
		assert.Error(t, err, "%q names something that exists nowhere and must be refused, not defaulted", bad)
	}
}

// TestParseSurfaceOverrides_TyposDoNotResolveToTheZeroValue is the reason both
// parsers return an error instead of a zero value. SurfaceContext is iota 0 and
// ApproachUnsafeFile is iota 0 — and unsafe-file is the LEAST safe approach, the
// one whose own doc calls choosing it a race acknowledgment. A parser that
// swallowed a typo would silently aim an override at the context surface and
// elect a well-known shared-cwd write nobody asked for.
func TestParseSurfaceOverrides_TyposDoNotResolveToTheZeroValue(t *testing.T) {
	_, err := parseSurfaceOverrides([]string{"kontext=hook"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "telepathy")
	assert.Contains(t, err.Error(), "kontext", "the error must name what the user actually typed")

	_, err = parseSurfaceOverrides([]string{"context=unsafe_file"})
	require.Error(t, err, "underscore is not the spelling; a near-miss must fail rather than resolve to iota 0")
}

// TestParseSurfaceOverrides_ErrorTextIsDerivedFromTheEnums pins that the "known"
// lists are generated, not restated. They were hand-written first, which is
// three copies of one enumeration and the shape that goes stale the next time a
// surface kind is added.
func TestParseSurfaceOverrides_ErrorTextIsDerivedFromTheEnums(t *testing.T) {
	_, err := parseSurfaceOverrides([]string{"nope=hook"})
	require.Error(t, err)
	for _, name := range agent.SurfaceKindNames() {
		assert.Contains(t, err.Error(), name,
			"every kind the enum declares must appear in the error a user reads")
	}

	_, err = parseSurfaceOverrides([]string{"context=nope"})
	require.Error(t, err)
	for _, name := range agent.ApproachNames() {
		assert.Contains(t, err.Error(), name,
			"every approach the enum declares must appear in the error a user reads")
	}
}

// TestParseSurfaceOverrides_ConflictingDuplicateIsRefused: a surface is
// delivered one way. Silently keeping the last pair would make
// `--surface context=hook --surface context=unsafe-file` do something the
// command line does not say.
func TestParseSurfaceOverrides_ConflictingDuplicateIsRefused(t *testing.T) {
	_, err := parseSurfaceOverrides([]string{"context=hook", "context=unsafe-file"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")

	// The same pair repeated is harmless and must NOT be an error: it says one
	// thing, twice.
	got, err := parseSurfaceOverrides([]string{"context=hook", "context=hook"})
	require.NoError(t, err)
	assert.Equal(t, agent.ApproachHook, got[agent.SurfaceContext])
}

// TestSurfaceHelpFor_DescribesEachEngineFromTheBackendsThemselves pins the
// property that makes this help worth having: it is COMPUTED, so it says
// different things about engines that behave differently. A hand-written table
// would pass any test that only checked the text was non-empty.
func TestSurfaceHelpFor_DescribesEachEngineFromTheBackendsThemselves(t *testing.T) {
	help := surfaceHelpFor([]string{"claude-code", "codex"})

	assert.Contains(t, help, "claude-code")
	assert.Contains(t, help, "codex")

	// claude-code's context surface offers three approaches; codex's offers only
	// the hook. If those render identically the table has stopped describing
	// anything.
	assert.Contains(t, help, "system-prompt", "claude-code's extra context approaches must show")
	assert.Contains(t, help, "(folded into another surface on this engine)",
		"codex folds MCP into its config surface; an empty row must SAY that rather than look like an oversight")
	assert.Contains(t, help, "(default)", "a reader has to be able to tell which approach is taken without asking")
}

// TestSurfaceHelpFor_NeverClaimsAnEngineCannotProduceAContextFile is a
// REGRESSION on a false statement this help actually shipped.
//
// It used to infer "cannot keep context as a file" from an engine whose context
// surface declares no unsafe-file approach, and told codex users there was no
// way to keep their assembled context as a document. codex writes AGENTS.md —
// verified by materializing one. The approach table names HOW a surface is
// delivered, not whether the user is left with a file, so it cannot answer that
// question and must never be asked it.
func TestSurfaceHelpFor_NeverClaimsAnEngineCannotProduceAContextFile(t *testing.T) {
	for _, engine := range []string{"codex", "claude-code", "kiro"} {
		help := strings.ToLower(surfaceHelpFor([]string{engine}))
		assert.NotContains(t, help, "no way to",
			"%s: help must not deny a capability it cannot see from the approach table", engine)
		assert.NotContains(t, help, "cannot deliver context",
			"%s: same claim, other wording", engine)
	}
}

// TestSurfaceHelpFor_PointsAtTheDefaultInvocationForKeepingContext: the useful
// answer to "how do I keep my context" is that materialize already does it, into
// whichever native file the engine reads. Pointing at an override instead would
// send a reader to a flag they do not need.
func TestSurfaceHelpFor_PointsAtTheDefaultInvocationForKeepingContext(t *testing.T) {
	for _, engine := range []string{"codex", "claude-code"} {
		help := surfaceHelpFor([]string{engine})
		assert.Contains(t, help, "materialize default --target ./keep",
			"%s: the no-flag invocation is the answer", engine)
	}
}

// TestSurfaceHelpFor_NoEnginesSaysSoRatherThanRenderingAnEmptyTable guards the
// empty case: a project before `config init`. An empty table reads as "this
// engine supports nothing", which is a different and alarming claim.
func TestSurfaceHelpFor_NoEnginesSaysSoRatherThanRenderingAnEmptyTable(t *testing.T) {
	help := surfaceHelpFor(nil)
	assert.Contains(t, help, "config init")
	assert.NotContains(t, help, "(default)")
}
