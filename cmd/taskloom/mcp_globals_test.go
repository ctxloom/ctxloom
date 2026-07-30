package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The MCP handlers resolve a store through the SAME package-level variables
// the CLI does — tasksProject (--project), tasksHoming (--homing) and
// rootCmd's persistent flag set. Those are launch-time values, and `taskloom
// mcp` is one long-lived process serving many calls, so the question these
// pin is whether "launch-time" and "per-call" can ever disagree here.
//
// They cannot: the variables are bound once by cobra before any handler runs
// and are never written again, so every call in a server's lifetime sees the
// same override — and it is the same override the CLI would see. That is not
// an accident to be rediscovered; it is the property that makes `taskloom
// --project X mcp` mean what an operator expects, and these hold it.
//
// The pins are written as MCP-versus-CLI equality rather than against a
// literal, because the value of the coupling is exactly that the two surfaces
// cannot drift apart. Decoupling the handlers from the globals later is
// legitimate; silently changing which store the MCP surface answers about is
// not, and only the first of those leaves these green.

// setProjectOverride sets the --project global for one test, restoring it
// after. It writes a package variable, which is what the row is about: there
// is no other seam, and a test that avoided it would not be testing the
// production path.
func setProjectOverride(t *testing.T, id string) {
	t.Helper()
	prev := tasksProject
	tasksProject = id
	t.Cleanup(func() { tasksProject = prev })
}

func setHomingOverride(t *testing.T, mode string) {
	t.Helper()
	prev := tasksHoming
	tasksHoming = mode
	t.Cleanup(func() { tasksHoming = prev })
}

// A launch-time --project reaches the MCP surface, and reaches it identically
// to the CLI. Without this the MCP server would answer about whatever project
// the cwd resolves to while the operator believes they pinned one.
func TestMCPHandlers_LaunchTimeProjectOverrideReachesTheHandlers(t *testing.T) {
	withProjectDir(t)
	setProjectOverride(t, "pinned-by-launch-flag")

	_, viaMCP, err := handleTaskList(context.Background(), nil, taskListInput{})
	require.NoError(t, err)
	assert.Equal(t, "pinned-by-launch-flag", viaMCP.ProjectID,
		"the --project the server was launched with must decide the store every MCP call reads")

	viaCLI, err := listTasksScoped(mustTaskContext(t), listOptions{})
	require.NoError(t, err)
	assert.Equal(t, viaCLI.ProjectID, viaMCP.ProjectID)
	assert.Equal(t, viaCLI.Path, viaMCP.Path,
		"the two surfaces resolve one store, or an agent and its operator are looking at different logs")
}

// The same for --homing, which chooses the store's LOCATION rather than its
// identity: a repo-homed server must read and write the log inside the repo,
// on both surfaces. A read that silently fell back to the private home store
// would show an empty list for tasks that were just written.
func TestMCPHandlers_LaunchTimeHomingOverrideReachesTheHandlers(t *testing.T) {
	proj := withProjectDir(t)
	// Repo homing means "the repo IS the store's identity", so the project
	// boundary must be real — the same condition a git checkout satisfies in
	// production. Without it the listing scope resolves to the global
	// aggregation, which by construction cannot see a repo-homed store; see
	// the note in the report accompanying this row.
	t.Setenv("CTXLOOM_ROOT", proj)
	setHomingOverride(t, "repo")

	_, added, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "written under repo homing"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(proj, ".taskloom", "tasks.jsonl"), added.Path,
		"a repo-homed write belongs in the repo, not the private home store")

	_, listed, err := handleTaskList(context.Background(), nil, taskListInput{})
	require.NoError(t, err)
	assert.Equal(t, added.Path, listed.Path,
		"task_add and task_list must agree on where the store is, or a write is invisible to the next read")
	require.Len(t, listed.Tasks, 1)
	assert.Equal(t, "written under repo homing", listed.Tasks[0].Text)

	viaCLI, err := listTasksScoped(mustTaskContext(t), listOptions{})
	require.NoError(t, err)
	assert.Equal(t, viaCLI.Path, listed.Path)
}
