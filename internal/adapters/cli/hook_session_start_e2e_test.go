package cli

import (
	"bytes"
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
func runSessionStartIn(t *testing.T, marker, payload string) claudeAnswer {
	t.Helper()
	t.Setenv(projectroot.EnvVar, "")
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, appDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte("schema_version: 8\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "default.yaml"),
		[]byte("description: seeded by the test\n"), 0o644))
	hash, err := agent.WriteContextFile(root, []*agent.Fragment{{Name: "rules", Content: marker}})
	require.NoError(t, err)
	cached, err := os.ReadFile(filepath.Join(root, agent.SCMContextSubdir, hash+".md"))
	require.NoError(t, err)
	require.Contains(t, string(cached), marker, "precondition: the project's context is on disk where a context hook would find it")
	testApp(t, configload.WithAppDir(appDir))
	t.Chdir(root)

	var out bytes.Buffer
	cmd := firedBy(t, &cobra.Command{}, claude.EngineName)
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetOut(&out)
	require.NoError(t, hookSessionStartCmd.RunE(cmd, nil))
	return decodeClaudeAnswer(t, out.Bytes())
}

// TestHookSessionStart_DeliversNoticesButNeverContext is the hook end to end
// on a resumed startup: the setup nudge reaches the user, and nothing arrives
// as the session's context — not the project's assembled context (already
// the session's system prompt), and not a summary of the resumed session,
// even when an older ctxloom left one in the session's output dir. Engines
// own compaction; the resume's transcript rides the assembled context.
func TestHookSessionStart_DeliversNoticesButNeverContext(t *testing.T) {
	testsupport.Isolate(t)
	const marker = "PROJECT-CONTEXT-MARKER-77ab"
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	out, err := mgr.RecordOutputDir(entry.HarpName, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(out, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "essence.md"), []byte("LEFTOVER-SUMMARY-3c1e\n"), 0o644))
	t.Setenv("CTXLOOM_RESUMED_FROM", entry.HarpName)
	t.Setenv("CTXLOOM_RESUMED_PARTS", "session")

	got := runSessionStartIn(t, marker, `{"session_id":"s-1","source":"startup"}`)

	assert.Nil(t, got.HookSpecificOutput, "no context is delivered")
	assert.NotEmpty(t, got.SystemMessage, "the setup nudge reaches the user")
	whole, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(whole), marker, "the project's context is never part of the hook's output")
	assert.NotContains(t, string(whole), "LEFTOVER-SUMMARY-3c1e", "a leftover summary file is never delivered")
}

// TestHookSessionStart_ClearNamesNoRecovery: after a /clear that displaced a
// bound transcript, the hook delivers no context and points at no ctxloom
// recovery — the engine's own /resume is the way back.
func TestHookSessionStart_ClearNamesNoRecovery(t *testing.T) {
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
	assert.NotContains(t, got.SystemMessage, "/recover")
	assert.NotContains(t, got.SystemMessage, marker)
}
