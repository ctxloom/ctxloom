package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
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

// TestListPipeline_CLIAndMCPAgreeOnEveryDecision pins the two surfaces to one
// pipeline. Everything except rendering is a shared decision — scope, store,
// filter, limit, attribution, hidden counts — and the pair has drifted twice
// before (--limit was silently ignored on one path; the tag-query hint on the
// other), each time because the decisions were written out twice.
func TestListPipeline_CLIAndMCPAgreeOnEveryDecision(t *testing.T) {
	withProjectDir(t)

	for _, text := range []string{"alpha task", "beta task", "gamma task"} {
		_, _, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: text})
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		name string
		in   taskListInput
	}{
		{"unfiltered", taskListInput{}},
		{"term filter", taskListInput{Term: "task"}},
		{"limited", taskListInput{Limit: 2}},
		{"term and limit", taskListInput{Term: "task", Limit: 1}},
		{"compact", taskListInput{Compact: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, viaMCP, err := handleTaskList(context.Background(), nil, tc.in)
			require.NoError(t, err)

			viaCLI, err := listTasksScoped(mustTaskContext(t), listOptions{
				Statuses:       tc.in.Statuses,
				Term:           tc.in.Term,
				TagQuery:       tc.in.TagQuery,
				All:            tc.in.IncludeCompleted,
				Global:         tc.in.Global,
				Limit:          tc.in.Limit,
				IncludeSummary: tc.in.IncludeSummary,
			})
			require.NoError(t, err)

			assert.Equal(t, viaCLI.Global, viaMCP.Global)
			assert.Equal(t, viaCLI.ProjectID, viaMCP.ProjectID)
			assert.Equal(t, viaCLI.OmittedByLimit, viaMCP.OmittedByLimit)
			assert.Equal(t, viaCLI.HiddenCompleted, viaMCP.HiddenCompleted)
			assert.Equal(t, viaCLI.HiddenDeferred, viaMCP.HiddenDeferred)

			wire := viaMCP.Tasks
			if tc.in.Compact {
				assert.Len(t, viaMCP.CompactTasks, len(viaCLI.Rows))
				return
			}
			require.Len(t, wire, len(viaCLI.Rows))
			for i := range wire {
				assert.Equal(t, viaCLI.Rows[i].HarpID, wire[i].HarpID)
				assert.Equal(t, viaCLI.Rows[i].ProjectID, wire[i].ProjectID)
			}
		})
	}
}
