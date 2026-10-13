package operations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestViewSession_CarriesTheRecordAndTheDerivedFacts: the view is the
// record's facts plus everything a renderer used to compute for itself —
// the essence, its staleness, the purge mark, the one clock.
func TestViewSession_CarriesTheRecordAndTheDerivedFacts(t *testing.T) {
	testsupport.Isolate(t)
	started := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	ended := started.Add(time.Hour)
	purged := ended.Add(time.Hour)
	out := t.TempDir()
	essence := filepath.Join(out, paths.EssenceFileName)
	require.NoError(t, os.WriteFile(essence, []byte("# essence\n"), 0o644))

	v := ViewSession(sessions.Entry{
		HarpName:     "swift-amber-falcon",
		ProjectDir:   "/proj",
		OutputDir:    out,
		Backend:      "claude-code",
		SessionID:    "native-1",
		Summary:      "Fixed the bug",
		StartedAt:    started,
		EndedAt:      &ended,
		PurgedAt:     &purged,
		LastActivity: ended,
	})

	assert.Equal(t, "swift-amber-falcon", v.Harp)
	assert.Equal(t, "/proj", v.Project)
	assert.Equal(t, "claude-code", v.Engine)
	assert.Equal(t, "native-1", v.NativeSession)
	assert.Equal(t, "Fixed the bug", v.Summary)
	assert.True(t, v.StartedAt.Equal(started))
	require.NotNil(t, v.EndedAt)
	assert.True(t, v.EndedAt.Equal(ended))
	assert.True(t, v.LastActivity.Equal(ended))
	assert.True(t, v.Purged)
	assert.True(t, v.Compacted)
	assert.Equal(t, essence, v.EssencePath)
	assert.False(t, v.StaleKnown, "no transcript to compare the essence against")
}

// TestViewSession_UncompactedHasNoEssence: a session never compacted has no
// essence path and is not purged; the view says so rather than guessing.
func TestViewSession_UncompactedHasNoEssence(t *testing.T) {
	testsupport.Isolate(t)

	v := ViewSession(sessions.Entry{HarpName: "never-compacted-harp"})

	assert.False(t, v.Compacted)
	assert.Empty(t, v.EssencePath)
	assert.False(t, v.Purged)
	assert.Nil(t, v.EndedAt)
}

// TestViewSessions_KeepsTheListingsOrder: the views come back in the order
// the listing handed the entries — the listing sorted by the one clock, and
// the view must not re-sort.
func TestViewSessions_KeepsTheListingsOrder(t *testing.T) {
	testsupport.Isolate(t)

	views := ViewSessions([]sessions.Entry{{HarpName: "b-harp"}, {HarpName: "a-harp"}}, nil)

	require.Len(t, views, 2)
	assert.Equal(t, "b-harp", views[0].Harp)
	assert.Equal(t, "a-harp", views[1].Harp)
	assert.NotNil(t, ViewSessions(nil, nil), "an empty listing is an empty slice, not nil")
}

// TestSessionJSON_SaysCompacted pins the "an essence exists" key on the
// operations session projections the CLI and MCP serialise.
func TestSessionJSON_SaysCompacted(t *testing.T) {
	const key = "compacted"
	for name, v := range map[string]any{
		"SessionView":  SessionView{Compacted: true},
		"SessionFacts": SessionFacts{Compacted: true},
	} {
		raw, err := json.Marshal(v)
		require.NoError(t, err, name)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(raw, &doc), name)
		assert.Equal(t, true, doc[key], "%s must report %q", name, key)
		assert.NotContains(t, doc, "distilled", name)
	}
}

// TestListSessionsInput_TakesCompactMissing pins list_sessions' input key.
func TestListSessionsInput_TakesCompactMissing(t *testing.T) {
	var in ListSessionsInput
	require.NoError(t, json.Unmarshal([]byte(`{"compact_missing": true}`), &in))
	assert.True(t, in.CompactMissing, "compact_missing must set the flag")
}

// TestViewSessions_StateComesFromTheProbe: a session's state is what the
// injected lock probe says, per harp, and nothing else: an EndedAt stamp (the
// last run's end, kept across a resume) does not make a live session ended,
// and its absence does not make an ended one live.
func TestViewSessions_StateComesFromTheProbe(t *testing.T) {
	testsupport.Isolate(t)
	ended := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	probe := map[string]string{
		"resumed-live-harp": SessionLive,
		"crashed-harp":      SessionEnded,
		"no-lock-harp":      SessionUnknown,
	}
	var asked []string
	views := ViewSessions([]sessions.Entry{
		{HarpName: "resumed-live-harp", EndedAt: &ended},
		{HarpName: "crashed-harp"},
		{HarpName: "no-lock-harp", EndedAt: &ended},
	}, func(harp string) string {
		asked = append(asked, harp)
		return probe[harp]
	})

	require.Len(t, views, 3)
	assert.Equal(t, SessionLive, views[0].State)
	assert.Equal(t, SessionEnded, views[1].State)
	assert.Equal(t, SessionUnknown, views[2].State)
	assert.Equal(t, []string{"resumed-live-harp", "crashed-harp", "no-lock-harp"}, asked, "one probe per row")
	require.NotNil(t, views[0].EndedAt, "the last run's end stamp is kept, not hidden, in the view")

	b, err := json.Marshal(views[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), `"state":"live"`)
}

// TestViewSessions_NoProbeNoState: a caller that passes no probe (MCP
// list_sessions, which runs inside a session's own process) gets no state and
// probes nothing — the field is absent from JSON rather than a guess.
func TestViewSessions_NoProbeNoState(t *testing.T) {
	testsupport.Isolate(t)
	views := ViewSessions([]sessions.Entry{{HarpName: "swift-amber-falcon"}}, nil)
	require.Len(t, views, 1)
	assert.Empty(t, views[0].State)
	b, err := json.Marshal(views[0])
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"state"`)
}

// TestLockSessionState_IsTheLocksThreeVerdicts: the production probe maps the
// session lock's three verdicts onto the three state words, one each — no
// lock file is unknown (never rounded to ended), a held lock is live, a free
// one is ended.
func TestLockSessionState_IsTheLocksThreeVerdicts(t *testing.T) {
	testsupport.Isolate(t)

	assert.Equal(t, SessionUnknown, LockSessionState("never-locked-harp"))

	require.NoError(t, sessionlock.Hold("held-harp"))
	t.Cleanup(func() { sessionlock.Release("held-harp") })
	assert.Equal(t, SessionLive, LockSessionState("held-harp"))

	require.NoError(t, sessionlock.Hold("ended-harp"))
	sessionlock.Release("ended-harp")
	assert.Equal(t, SessionEnded, LockSessionState("ended-harp"))
}
