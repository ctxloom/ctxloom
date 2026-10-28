package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	taskloomconfig "github.com/ctxloom/ctxloom/internal/taskloom/config"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// rustTagPermitsConfig writes Rust taskloom's own opted-in
// .taskloom/config.yaml (the config package's fixture, verbatim) as dir's
// project config.
func rustTagPermitsConfig(t *testing.T, dir string) {
	t.Helper()
	root, err := sourcedir.RepoRoot()
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(root, "internal", "taskloom", "config", "testdata", "rust-tag-permits.config.yaml"))
	require.NoError(t, err)
	full := filepath.Join(dir, taskloomconfig.DirName, taskloomconfig.FileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, body, 0o644))
}

// TestTagPermits_CLIListAddAndTagIgnoreTheBlock drives the CLI end to end in
// a project whose config carries Rust taskloom's tag_permits: list, add and
// tag all work, and a tag those permits do not permit (a flat tag, an area
// key the closed list omits) is written anyway. Go taskloom stays permissive
// about tags; the permits are Rust taskloom's to enforce.
func TestTagPermits_CLIListAddAndTagIgnoreTheBlock(t *testing.T) {
	rustTagPermitsConfig(t, taskstest.ProjectDir(t))

	_, err := executeTaskloom(t, "list", "--format", "json")
	require.NoError(t, err, "list must not refuse a config carrying tag_permits")

	out, err := executeAdd(t, "under rust permits", "--tag", "urgent")
	require.NoError(t, err, "add must not refuse a config carrying tag_permits; out:\n%s", out)

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{Term: "under rust permits"})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	harpID := res.Tasks[0].HarpID

	_, err = executeTag(t, harpID, "--add", "area:nowhere", "--format", "text")
	require.NoError(t, err, "tag must not refuse a config carrying tag_permits")

	got := storeBytes(t)
	assert.Contains(t, got, `"urgent"`, "a flat tag the permits omit is still written")
	assert.Contains(t, got, `"area:nowhere"`, "a key a closed namespace omits is still written")
}

// TestTagPermits_MCPListAddAndTagIgnoreTheBlock is the MCP surface's half:
// the same three operations through the task_* handlers.
func TestTagPermits_MCPListAddAndTagIgnoreTheBlock(t *testing.T) {
	rustTagPermitsConfig(t, taskstest.ProjectDir(t))

	_, _, err := handleTaskList(context.Background(), nil, taskListInput{})
	require.NoError(t, err)

	_, added, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "via mcp", Tags: []string{"urgent"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"urgent"}, added.Task.Tags)

	_, tagged, err := handleTaskTag(context.Background(), nil, taskTagInput{HarpID: added.Task.HarpID, Add: []string{"area:nowhere"}})
	require.NoError(t, err)
	assert.Contains(t, tagged.Task.Tags, "area:nowhere")
}

// TestTagPermits_MalformedBlockStillRefusesTheCommand pins the other side: a
// tag_permits block of the wrong shape is a config error, as any other
// malformed key is.
func TestTagPermits_MalformedBlockStillRefusesTheCommand(t *testing.T) {
	writeConfigForTest(t, taskstest.ProjectDir(t), "tag_permits:\n  namespaces:\n    area: closed\n")

	_, err := executeTaskloom(t, "list", "--format", "json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config error")
	assert.Contains(t, err.Error(), "tag_permits")
}
