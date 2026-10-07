package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// runSessionStartIn builds a project with profiles but no agents (so the
// setup nudge fires) whose regenerated context cache holds marker — the
// project context the session-start hook must never deliver. It runs the
// hook against that project with payload on stdin, returning its output.
func runSessionStartIn(t *testing.T, marker, payload string) HookOutput {
	t.Helper()
	t.Setenv(projectroot.EnvVar, "")
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, appDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte("schema_version: 7\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "default.yaml"),
		[]byte("description: seeded by the test\n"), 0o644))
	hash, err := agent.WriteContextFile(root, []*agent.Fragment{{Name: "rules", Content: marker}})
	require.NoError(t, err)
	cached, err := agent.ReadContextFile(root, hash)
	require.NoError(t, err)
	require.Contains(t, cached, marker, "precondition: the project's context is on disk where a context hook would find it")
	testApp(t, configload.WithAppDir(appDir))
	t.Chdir(root)

	stdinFromString(t, payload)
	var runErr error
	out := captureStdout(t, func() { runErr = hookSessionStartCmd.RunE(&cobra.Command{}, nil) })
	require.NoError(t, runErr)
	var got HookOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got), "stdout is the hook envelope: %q", out)
	return got
}

// TestHookSessionStart_DeliversTheResumedEssenceAndNoticesButNeverTheProjectContext
// is the hook end to end on a compacted resume's startup: the resumed
// session's essence arrives as the session's context, the setup nudge as the
// user's notice, and the project's assembled context — already the session's
// system prompt — appears in neither.
func TestHookSessionStart_DeliversTheResumedEssenceAndNoticesButNeverTheProjectContext(t *testing.T) {
	testsupport.Isolate(t)
	const marker = "PROJECT-CONTEXT-MARKER-77ab"
	harp := "swift-amber-falcon"
	essencePath, err := harpEssencePath(t, harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(essencePath, []byte("what we did last time\n"), 0o644))
	t.Setenv("CTXLOOM_RESUMED_FROM", harp)
	t.Setenv("CTXLOOM_RESUMED_PARTS", "session")

	got := runSessionStartIn(t, marker, `{"session_id":"s-1","source":"startup"}`)

	require.NotNil(t, got.HookSpecificOutput, "the resumed essence is delivered")
	assert.Equal(t, claude.HookEventSessionStart, got.HookSpecificOutput.HookEventName)
	assert.Contains(t, got.HookSpecificOutput.AdditionalContext, "what we did last time", "the compacted essence arrives")
	assert.NotEmpty(t, got.SystemMessage, "the setup nudge reaches the user")
	whole, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(whole), marker, "the project's context is never part of the hook's output")
}

// TestHookSessionStart_ClearNamesRecoverAndDeliversNoEssence: after a /clear
// that displaced a bound transcript, the user is told /recover brings it
// back, and nothing is delivered as context — not the essence (a /clear is
// not a resume) and not the project's context.
func TestHookSessionStart_ClearNamesRecoverAndDeliversNoEssence(t *testing.T) {
	testsupport.Isolate(t)
	const marker = "PROJECT-CONTEXT-MARKER-91fd"
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(entry.HarpName, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "pre-clear-id", "/pre-clear.jsonl"))
	t.Setenv(agent.SessionHarpEnv, entry.HarpName)

	got := runSessionStartIn(t, marker, `{"session_id":"post-clear-id","source":"clear"}`)

	assert.Nil(t, got.HookSpecificOutput, "a /clear delivers no context")
	assert.Contains(t, got.SystemMessage, "/recover", "the /clear notice names /recover")
	assert.True(t, strings.Contains(got.SystemMessage, "\n\n"), "the setup nudge rides beside the /clear notice: %q", got.SystemMessage)
	assert.NotContains(t, got.SystemMessage, marker)
}
