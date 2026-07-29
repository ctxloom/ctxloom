package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clifmt"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestGlobalNotice_CLIAndMCPComposeTheSameText pins the two list surfaces to
// one global-scope disclosure. The composition is the same two-part rule in
// both — the fallback explanation, then the aggregation limitation — and a
// user who is told their repo-homed project is invisible on one surface must
// be told on the other.
func TestGlobalNotice_CLIAndMCPComposeTheSameText(t *testing.T) {
	// Case 1: explicit --global from inside a repo-homed project. The scope
	// itself carries no notice, so the whole text is the limitation.
	proj := taskstest.ProjectDir(t)
	writeConfigForTest(t, proj, "homing: repo\n")

	var stdout, stderr strings.Builder
	require.NoError(t, runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Global: true, Format: clifmt.FormatText}))

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{Global: true})
	require.NoError(t, err)
	require.NotEmpty(t, res.Notice)

	assert.Contains(t, stderr.String(), res.Notice,
		"the CLI's stderr notice must be the MCP result's Notice, not a separately-composed near-copy")

	// The composition itself, isolated: both surfaces call it.
	scope, err := resolveListScope(true, "", proj)
	require.NoError(t, err)
	assert.Equal(t, res.Notice, composeGlobalNotice(scope, proj))
}

// TestComposeGlobalNotice_JoinsFallbackAndLimitation pins the join: when the
// scope explains a fallback, the limitation is appended to it rather than
// replacing it — dropping either half loses information the caller needs.
func TestComposeGlobalNotice_JoinsFallbackAndLimitation(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()

	withFallback := composeGlobalNotice(listScope{Global: true, Notice: "no project detected in " + dir}, dir)
	assert.Contains(t, withFallback, "no project detected in "+dir)
	assert.Contains(t, withFallback, "; ")
	assert.Contains(t, withFallback, globalScopeGeneralLimitation)

	explicit := composeGlobalNotice(listScope{Global: true}, dir)
	assert.Equal(t, globalScopeGeneralLimitation, explicit,
		"an explicit --global has nothing to explain, so the notice is the limitation alone")
}
