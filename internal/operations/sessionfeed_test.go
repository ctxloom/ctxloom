package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentbus"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const feedWait = 5 * time.Second

// claudeFixture is a minimal claude-code transcript (two main-thread turns)
// for the by-location store paths.
const claudeFixture = `{"type":"user","timestamp":"2026-06-01T10:00:01Z","message":{"role":"user","content":"stored question"}}
{"type":"assistant","timestamp":"2026-06-01T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"stored answer"}]}}`

// seedFeedHarp mints an index entry; withTranscript drops the claude fixture
// into the harp's persist/ store (the by-location association).
func seedFeedHarp(t *testing.T, home string, withTranscript bool) string {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	if withTranscript {
		p := filepath.Join(home, ".ctxloom", "sessions", entry.HarpName,
			"persist", "transcripts", "-proj-enc", "uuid-1.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(claudeFixture), 0o644))
	}
	return entry.HarpName
}

// liveBus stands up an observable bus socket, points CTXLOOM_BUS_SOCKET at it,
// and tees a fake child stream for harp. The returned in channel feeds the
// child's events; out is the "orchestrator's" side and MUST be drained by the
// test (draining it synchronizes the publishes).
func liveBus(t *testing.T, harp string) (in chan agent.ChatEvent, out <-chan agent.ChatEvent) {
	t.Helper()
	hub := agentbus.NewTapHub()
	sock := filepath.Join(t.TempDir(), "bus.sock")
	srv, err := agentbus.Listen(sock, agentbus.New(agentbus.Hooks{}), hub, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv(agentbus.SocketEnv, sock)

	in = make(chan agent.ChatEvent)
	out = hub.Tee(harp, in)
	return in, out
}

// nextFeedEvent receives one feed event with a deadline.
func nextFeedEvent(t *testing.T, events <-chan SessionFeedEvent) SessionFeedEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		require.True(t, ok, "feed closed early")
		return ev
	case <-time.After(feedWait):
		t.Fatal("no feed event arrived")
		return SessionFeedEvent{}
	}
}

func feedEntryContent(t *testing.T, fe SessionFeedEvent) string {
	t.Helper()
	require.NotNil(t, fe.Event, "expected a watch event, got a gap")
	e, ok := fe.Event.GetEvent().(*pb.WatchEvent_Entry)
	require.True(t, ok, "expected an entry event, got %T", fe.Event.GetEvent())
	return e.Entry.GetContent()
}

func TestParseFeedSource(t *testing.T) {
	for _, ok := range []string{"", "auto", "live", "store"} {
		_, err := ParseFeedSource(ok)
		assert.NoError(t, err, "source %q", ok)
	}
	_, err := ParseFeedSource("psychic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown feed source")
}

// TestWatchSessionFeed_AutoPrefersLive: with an orchestrator holding the harp
// live, auto resolves to the live tap and normalizes ChatEvents onto the
// WatchEvent vocabulary — entries as they happen, a boundary from Complete,
// and a clean end when the child's stream closes.
func TestWatchSessionFeed_AutoPrefersLive(t *testing.T) {
	testsupport.Isolate(t)
	harp := seedFeedHarp(t, os.Getenv("HOME"), false)
	in, out := liveBus(t, harp)

	feed, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "live", feed.Source)

	go func() { in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "live words"}} }()
	<-out
	assert.Equal(t, "live words", feedEntryContent(t, nextFeedEvent(t, feed.Events)))

	go func() { in <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}} }()
	<-out
	fe := nextFeedEvent(t, feed.Events)
	require.NotNil(t, fe.Event)
	b, ok := fe.Event.GetEvent().(*pb.WatchEvent_Boundary)
	require.True(t, ok, "a live Complete maps to a boundary")
	assert.Equal(t, int32(0), b.Boundary.GetFromIndex())
	assert.Equal(t, int32(1), b.Boundary.GetToIndex())

	close(in)
	select {
	case _, open := <-feed.Events:
		assert.False(t, open, "a live feed ends when the child's stream closes")
	case <-time.After(feedWait):
		t.Fatal("feed never closed after the child ended")
	}
	require.NoError(t, <-feed.Errs)
}

// TestWatchSessionFeed_LiveStitchesScrollback: a live-tapped harp with a
// recorded transcript replays the stored entries (plus one boundary) as
// scrollback before the live events.
func TestWatchSessionFeed_LiveStitchesScrollback(t *testing.T) {
	testsupport.Isolate(t)
	harp := seedFeedHarp(t, os.Getenv("HOME"), true)
	in, out := liveBus(t, harp)
	defer close(in)

	feed, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "live", feed.Source)

	assert.Equal(t, "stored question", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
	assert.Equal(t, "stored answer", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
	fe := nextFeedEvent(t, feed.Events)
	b, ok := fe.Event.GetEvent().(*pb.WatchEvent_Boundary)
	require.True(t, ok, "scrollback closes with a boundary")
	assert.Equal(t, int32(2), b.Boundary.GetToIndex())

	go func() { in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "and now live"}} }()
	<-out
	assert.Equal(t, "and now live", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchSessionFeed_AutoFallsBackToStore: no live tap anywhere → the S0
// store tail feeds, transparently.
func TestWatchSessionFeed_AutoFallsBackToStore(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed, err := WatchSessionFeed(ctx, SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "store", feed.Source)

	assert.Equal(t, "stored question", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
	assert.Equal(t, "stored answer", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchSessionFeed_ForcedStoreIgnoresLive: --source store must not touch
// a live tap even when one is available.
func TestWatchSessionFeed_ForcedStoreIgnoresLive(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, true)
	in, _ := liveBus(t, harp)
	defer close(in)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed, err := WatchSessionFeed(ctx, SessionFeedRequest{Harp: harp, Source: FeedSourceStore})
	require.NoError(t, err)
	assert.Equal(t, "store", feed.Source)
	assert.Equal(t, "stored question", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchSessionFeed_ForcedLiveErrorsWhenNotLive: --source live is a hard
// error when no orchestrator holds the harp — no silent store fallback.
func TestWatchSessionFeed_ForcedLiveErrorsWhenNotLive(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, true)

	_, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceLive})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no live tap")
}

// TestWatchSessionFeed_LiveNotHoldingHarpFallsBack: a reachable orchestrator
// that does NOT hold the harp answers the typed not-live — auto moves on to
// the store.
func TestWatchSessionFeed_LiveNotHoldingHarpFallsBack(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, true)
	in, _ := liveBus(t, "some-other-child") // live, but for a different harp
	defer close(in)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed, err := WatchSessionFeed(ctx, SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "store", feed.Source)
	assert.Equal(t, "stored question", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchFeed_DirDiscovery: with no ambient env, the resolver finds the
// orchestrator through the session-dir socket convention
// (<sessions>/<harp>/agent-bus.sock). Short name: the socket path must fit
// sun_path even under the test tempdir.
func TestWatchFeed_DirDiscovery(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, false)

	// The orchestrator's socket lives under its OWN (coordinator) harp dir.
	coordDir := filepath.Join(home, ".ctxloom", "sessions", "co")
	require.NoError(t, os.MkdirAll(coordDir, 0o755))
	hub := agentbus.NewTapHub()
	srv, err := agentbus.Listen(filepath.Join(coordDir, "agent-bus.sock"), agentbus.New(agentbus.Hooks{}), hub, nil)
	require.NoError(t, err)
	defer srv.Close()

	in := make(chan agent.ChatEvent)
	out := hub.Tee(harp, in)
	defer close(in)

	feed, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "live", feed.Source)

	go func() { in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "found you"}} }()
	<-out
	assert.Equal(t, "found you", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchSessionFeed_UnknownHarp / NothingToWatch pin the resolver's error
// surface (same texts the S0 command promised).
func TestWatchSessionFeed_ErrorSurface(t *testing.T) {
	home := testsupport.Isolate(t)

	_, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: "no-such-harp"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harp not found")

	bare := seedFeedHarp(t, home, false) // no live tap, no transcript, no session id
	_, err = WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: bare})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to watch")
}

// TestAdaptLiveFeed_GapAndBoundaries drives the normalization core directly:
// gap markers pass through as explicit feed events, boundaries count
// feed-relative indexes across turns, and an empty turn emits no boundary.
func TestAdaptLiveFeed_GapAndBoundaries(t *testing.T) {
	testsupport.Isolate(t)
	obs := make(chan agentbus.ObserveEvent, 8)
	obsErrs := make(chan error, 1)
	obs <- agentbus.ObserveEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "one"}}
	obs <- agentbus.ObserveEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	obs <- agentbus.ObserveEvent{Gap: 5}
	obs <- agentbus.ObserveEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "two"}}
	obs <- agentbus.ObserveEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	obs <- agentbus.ObserveEvent{Complete: &agent.TurnMeta{StopReason: "cancelled"}} // empty turn: no boundary
	obs <- agentbus.ObserveEvent{Ended: true}
	close(obs)
	close(obsErrs)

	entry := &sessions.Entry{HarpName: "fake-harp"} // no transcript → no scrollback
	events, errs := adaptLiveFeed(context.Background(), entry, "claude-code", obs, obsErrs)

	var got []SessionFeedEvent
	for ev := range events {
		got = append(got, ev)
	}
	require.NoError(t, <-errs)
	require.Len(t, got, 5)

	assert.Equal(t, "one", feedEntryContent(t, got[0]))
	b1 := got[1].Event.GetEvent().(*pb.WatchEvent_Boundary).Boundary
	assert.Equal(t, int32(0), b1.GetFromIndex())
	assert.Equal(t, int32(1), b1.GetToIndex())

	assert.Equal(t, 5, got[2].Gap, "the gap marker is explicit in the unified feed")
	assert.Nil(t, got[2].Event)

	assert.Equal(t, "two", feedEntryContent(t, got[3]))
	b2 := got[4].Event.GetEvent().(*pb.WatchEvent_Boundary).Boundary
	assert.Equal(t, int32(1), b2.GetFromIndex())
	assert.Equal(t, int32(2), b2.GetToIndex())
}
