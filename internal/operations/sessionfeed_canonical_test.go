package operations

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// seedCanonicalFeedHarp mints an index entry and records one real assistant
// turn to its canonical transcript (via the actual transcript.Recorder
// writer), then re-reads the entry through the index so
// CanonicalTranscriptPath comes back populated exactly as production code
// sees it (Manager.Find -> fillCanonicalTranscript, tough-cloud S4).
// Deliberately leaves SessionID/TranscriptPath unbound: only the canonical
// locator should ever be consulted for this entry.
func seedCanonicalFeedHarp(t *testing.T, content string) *sessions.Entry {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	minted, err := mgr.AssignHarp("/proj", "codex")
	require.NoError(t, err)

	rec, err := transcript.NewRecorder(minted.HarpName, "codex")
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{
		Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: content},
	}))
	require.NoError(t, rec.Close())

	entry, err := mgr.Find(minted.HarpName)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.NotEmpty(t, entry.CanonicalTranscriptPath, "fillCanonicalTranscript must populate the path once the recorder has written")
	require.Empty(t, entry.SessionID, "test wants the SessionID locator unbound so only canonical can serve this entry")
	require.Empty(t, entry.TranscriptPath, "test wants the by-location locator unbound so only canonical can serve this entry")
	return entry
}

// TestFeedScrollback_ReadsCanonical proves the session-watch scrollback path
// (tough-cloud S4) reads a captured canonical transcript directly — no
// backend session id, no by-location transcript, nothing but
// CanonicalTranscriptPath — and recovers the real payload, not an empty
// scrollback.
func TestFeedScrollback_ReadsCanonical(t *testing.T) {
	testsupport.Isolate(t)
	entry := seedCanonicalFeedHarp(t, "SCROLLBACK-REAL-PAYLOAD")

	entries := feedScrollback(context.Background(), entry, "codex")
	require.Len(t, entries, 1)
	assert.Equal(t, "SCROLLBACK-REAL-PAYLOAD", entries[0].Content)
}

// TestWatchStoreFeed_StreamsCanonical proves watchStoreFeed's canonical
// branch (pb.WatchCanonicalTranscript) actually streams the real captured
// entry as a WatchEvent, not just that it returns without error.
func TestWatchStoreFeed_StreamsCanonical(t *testing.T) {
	testsupport.Isolate(t)
	entry := seedCanonicalFeedHarp(t, "WATCH-REAL-PAYLOAD")

	ctx, cancel := context.WithTimeout(context.Background(), feedWait)
	defer cancel()

	feed, err := watchStoreFeed(ctx, entry, "codex")
	require.NoError(t, err)
	assert.Equal(t, "store", feed.Source)

	select {
	case ev := <-feed.Events:
		require.NotNil(t, ev.Event)
		e := ev.Event.GetEntry()
		require.NotNil(t, e, "first event must be the captured entry, got %+v", ev.Event)
		assert.Equal(t, "WATCH-REAL-PAYLOAD", e.GetContent())
	case <-time.After(feedWait):
		t.Fatal("timed out waiting for the canonical entry to stream")
	}
}
