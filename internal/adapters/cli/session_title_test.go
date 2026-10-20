package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// recordPrompts mints a session in projectDir and records prompts to its
// canonical transcript as the user's, through the real recorder.
func recordPrompts(t *testing.T, projectDir string, prompts ...string) string {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	e, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	recordPromptsFor(t, e.HarpName, prompts...)
	return e.HarpName
}

// recordPromptsFor records prompts to harp's canonical transcript as the
// user's, through the real recorder.
func recordPromptsFor(t *testing.T, harp string, prompts ...string) {
	t.Helper()
	if len(prompts) == 0 {
		return
	}
	rec, err := transcript.NewRecorder(safefs.New(), harp, "claude-code")
	require.NoError(t, err)
	for _, p := range prompts {
		require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: p}}))
	}
	require.NoError(t, rec.Close())
}

// The `summary` a listing carries — in JSON (what an editor's session picker
// reads) and in the table — is the session's first user prompt, and
// `session search` matches it.
func TestSessionList_SummaryIsTheFirstUserPrompt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	titled := recordPrompts(t, dir, "Root-cause the retry-backoff-overflow flake\nwith detail", "and then this")
	untitled := recordPrompts(t, dir)

	out, err := runSessionCmd(t, "session", "list", "--format", "json")
	require.NoError(t, err)
	rows := jsonRowsByHarp(t, out)
	assert.Equal(t, "Root-cause the retry-backoff-overflow flake", rows[titled]["summary"])
	assert.Equal(t, "(no summary)", rows[untitled]["summary"])

	out, err = runSessionCmd(t, "session", "list", "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, "Root-cause the retry-backoff-overflow flake")

	out, err = runSessionCmd(t, "session", "search", "backoff-overflow", "--format", "json")
	require.NoError(t, err)
	rows = jsonRowsByHarp(t, out)
	require.Len(t, rows, 1)
	assert.Contains(t, rows, titled)
}
