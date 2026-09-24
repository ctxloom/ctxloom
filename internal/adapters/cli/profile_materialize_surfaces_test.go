package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines"
)

func TestParseSurfaceOverrides_ParsesPairsAndRejectsNamesThatExistNowhere(t *testing.T) {
	got, err := parseSurfaceOverrides([]string{"context=unsafe-file", "skills=hook"})
	require.NoError(t, err)
	assert.Equal(t, map[agent.SurfaceKind]string{
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

// TestParseSurfaceOverrides_TyposDoNotResolveToTheZeroValue is the reason the
// kind parser returns an error instead of a zero value — SurfaceContext is
// iota 0, so a swallowed typo would silently aim an override at the context
// surface — and the reason an approach name is checked against what SOME
// registered engine declares: a near-miss must fail rather than travel on as
// a name no engine can construct.
func TestParseSurfaceOverrides_TyposDoNotResolveToTheZeroValue(t *testing.T) {
	_, err := parseSurfaceOverrides([]string{"kontext=hook"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "telepathy")
	assert.Contains(t, err.Error(), "kontext", "the error must name what the user actually typed")

	_, err = parseSurfaceOverrides([]string{"context=unsafe_file"})
	require.Error(t, err, "underscore is not the spelling; a near-miss must fail rather than resolve to iota 0")
}

// TestParseSurfaceOverrides_ErrorTextIsDerivedFromTheDeclarations pins that
// the "known" lists are generated, not restated: the kinds from the enum, the
// approach names from every registered engine's Declaration. They were
// hand-written first, which is three copies of one enumeration and the shape
// that goes stale the next time a surface kind or an approach is added.
func TestParseSurfaceOverrides_ErrorTextIsDerivedFromTheDeclarations(t *testing.T) {
	_, err := parseSurfaceOverrides([]string{"nope=hook"})
	require.Error(t, err)
	for _, name := range agent.SurfaceKindNames() {
		assert.Contains(t, err.Error(), name,
			"every kind the enum declares must appear in the error a user reads")
	}

	_, err = parseSurfaceOverrides([]string{"context=nope"})
	require.Error(t, err)
	known := operations.KnownApproachNames(engines.Registry())
	require.NotEmpty(t, known)
	for _, name := range known {
		assert.Contains(t, err.Error(), name,
			"every approach some engine declares must appear in the error a user reads")
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
