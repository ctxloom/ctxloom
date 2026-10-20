package operations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedTitledHarp mints a session and records entries to its canonical
// transcript through the real recorder, returning the entry as the index
// reads it back (CanonicalTranscriptPath populated).
func seedTitledHarp(t *testing.T, entries ...agent.SessionEntry) sessions.Entry {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	minted, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	if len(entries) > 0 {
		rec, err := transcript.NewRecorder(safefs.New(), minted.HarpName, "claude-code")
		require.NoError(t, err)
		for i := range entries {
			require.NoError(t, rec.Record(agent.ChatEvent{Entry: &entries[i]}))
		}
		require.NoError(t, rec.Close())
	}
	entry, err := mgr.Find(minted.HarpName)
	require.NoError(t, err)
	require.NotNil(t, entry)
	return *entry
}

// A session's title is the first thing its user asked: the first main-thread
// user prompt in ctxloom's canonical transcript, cut to its first non-empty
// line and 80 characters. What came before it (an assistant greeting) and
// what a subagent was asked (a sidechain prompt) are not the user's words.
func TestViewSession_TitleIsTheFirstUserPrompt(t *testing.T) {
	testsupport.Isolate(t)
	long := strings.Repeat("y", 120)
	e := seedTitledHarp(t,
		agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hello, how can I help?"},
		agent.SessionEntry{Type: agent.EntryTypeUser, Content: "subagent brief", Sidechain: true},
		agent.SessionEntry{Type: agent.EntryTypeUser, Content: "   "},
		agent.SessionEntry{Type: agent.EntryTypeUser, Content: "\n  fix the flaky purge test " + long + "\nsecond line"},
		agent.SessionEntry{Type: agent.EntryTypeUser, Content: "a later prompt"},
	)

	got := ViewSession(e).Title
	assert.Equal(t, sessions.FirstLineSummary("fix the flaky purge test "+long), got)
	assert.True(t, strings.HasPrefix(got, "fix the flaky purge test "), got)
	assert.LessOrEqual(t, len([]rune(got)), 80)
}

// No canonical transcript, or one with no user prompt in it, is no title —
// never an error and never a placeholder at this layer.
func TestViewSession_NoUserPromptIsNoTitle(t *testing.T) {
	testsupport.Isolate(t)
	assert.Empty(t, SessionTitle(sessions.Entry{}), "no transcript path at all")
	assert.Empty(t, ViewSession(seedTitledHarp(t)).Title)
	assert.Empty(t, ViewSession(seedTitledHarp(t,
		agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "only me"})).Title)
}
