package operations

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/sessions"
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

// fakeConsumerServer is a hermetic double for agentcoord.v1.ConsumerService
// (D1/D2): no real coordinator, no journals — just enough to drive the
// discovery + watch adapter under test, the same level of realism the
// retired agentbus tests used for their fake TapHub-backed bus server.
type fakeConsumerServer struct {
	agentcoordpb.UnimplementedConsumerServiceServer

	mu   sync.Mutex
	runs []*agentcoordpb.ListRunsResult_RunInfo
	subs map[chan *agentcoordpb.AgentEvent]struct{}
}

func newFakeConsumerServer() *fakeConsumerServer {
	return &fakeConsumerServer{subs: map[chan *agentcoordpb.AgentEvent]struct{}{}}
}

func (f *fakeConsumerServer) setRuns(runs ...*agentcoordpb.ListRunsResult_RunInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = runs
}

func (f *fakeConsumerServer) ListRuns(context.Context, *agentcoordpb.ListRunsRequest) (*agentcoordpb.ListRunsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &agentcoordpb.ListRunsResult{Runs: f.runs}, nil
}

func (f *fakeConsumerServer) WatchRuns(_ *agentcoordpb.WatchRunsRequest, stream grpc.ServerStreamingServer[agentcoordpb.WatchEvent]) error {
	ch := make(chan *agentcoordpb.AgentEvent, 32)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}()

	if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Snapshot{Snapshot: &agentcoordpb.RosterSnapshot{}}}); err != nil {
		return err
	}
	for {
		select {
		case ev := <-ch:
			if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Event{Event: ev}}); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// push broadcasts ev to every live WatchRuns subscriber and blocks until at
// least one subscriber's channel accepted it — synchronizing the test with
// delivery (mirrors the retired liveBus helper's "drain out" synchronization).
func (f *fakeConsumerServer) push(t *testing.T, ev *agentcoordpb.AgentEvent) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		n := len(f.subs)
		f.mu.Unlock()
		return n > 0
	}, feedWait, 5*time.Millisecond, "no WatchRuns subscriber attached")
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		ch <- ev
	}
}

// startFakeCoordinator serves f over a real loopback gRPC listener and
// writes the endpoint.json a fresh HOME needs for internal/agentcoord/
// discover to find it (the D1/D2 discovery mechanism the retired agentbus
// socket-glob convention is replaced by).
func startFakeCoordinator(t *testing.T, home, projectKey string, f *fakeConsumerServer) {
	t.Helper()
	srv := grpc.NewServer()
	agentcoordpb.RegisterConsumerServiceServer(srv, f)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	port := ln.Addr().(*net.TCPAddr).Port
	dir := filepath.Join(home, ".ctxloom", "coord", projectKey)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body := fmt.Sprintf(`{"loopback_port":%d,"consumer_cred":"test-cred"}`, port)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte(body), 0o600))
}

// runInfoFor is the minimal ListRuns roster row watchConsumerFeed's by-harp
// scan needs.
func runInfoFor(harp, runID string) *agentcoordpb.ListRunsResult_RunInfo {
	return &agentcoordpb.ListRunsResult_RunInfo{
		RunId: runID,
		Agent: &agentcoordpb.AgentIdentity{AgentId: harp},
	}
}

// assistantMessage pushes a complete one-shot MessageStarted/Delta/Completed
// sequence — the shape enginehost.go's adapt() would have produced for one
// content chunk.
func assistantMessage(t *testing.T, f *fakeConsumerServer, id, text string) {
	t.Helper()
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{
		MessageId: id, Role: agentcoordpb.MessageRole_MESSAGE_ROLE_ASSISTANT, Channel: agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL,
	}}})
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{
		MessageId: id, Text: text,
	}}})
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageCompleted{MessageCompleted: &agentcoordpb.MessageCompleted{
		MessageId: id,
	}}})
}

func turnIdle(t *testing.T, f *fakeConsumerServer) {
	t.Helper()
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: customEventTurnIdle}}})
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

// TestWatchSessionFeed_AutoPrefersLive: with a coordinator holding the harp
// live, auto resolves to the live tap and normalizes AgentEvents onto the
// WatchEvent vocabulary — entries as they happen, a boundary from
// ctxloom/turn_idle, and a clean end on RunCompleted.
func TestWatchSessionFeed_AutoPrefersLive(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, false)
	f := newFakeConsumerServer()
	f.setRuns(runInfoFor(harp, "run-1"))
	startFakeCoordinator(t, home, "proj", f)

	feed, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "live", feed.Source)

	assistantMessage(t, f, "m-1", "live words")
	assert.Equal(t, "live words", feedEntryContent(t, nextFeedEvent(t, feed.Events)))

	turnIdle(t, f)
	fe := nextFeedEvent(t, feed.Events)
	require.NotNil(t, fe.Event)
	b, ok := fe.Event.GetEvent().(*pb.WatchEvent_Boundary)
	require.True(t, ok, "a live turn_idle maps to a boundary")
	assert.Equal(t, int32(0), b.Boundary.GetFromIndex())
	assert.Equal(t, int32(1), b.Boundary.GetToIndex())

	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}})
	select {
	case _, open := <-feed.Events:
		assert.False(t, open, "a live feed ends on RunCompleted")
	case <-time.After(feedWait):
		t.Fatal("feed never closed after RunCompleted")
	}
	require.NoError(t, <-feed.Errs)
}

// TestWatchSessionFeed_LiveStitchesScrollback: a live-tapped harp with a
// recorded transcript replays the stored entries (plus one boundary) as
// scrollback before the live events.
func TestWatchSessionFeed_LiveStitchesScrollback(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, true)
	f := newFakeConsumerServer()
	f.setRuns(runInfoFor(harp, "run-1"))
	startFakeCoordinator(t, home, "proj", f)

	feed, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "live", feed.Source)

	assert.Equal(t, "stored question", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
	assert.Equal(t, "stored answer", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
	fe := nextFeedEvent(t, feed.Events)
	b, ok := fe.Event.GetEvent().(*pb.WatchEvent_Boundary)
	require.True(t, ok, "scrollback closes with a boundary")
	assert.Equal(t, int32(2), b.Boundary.GetToIndex())

	assistantMessage(t, f, "m-1", "and now live")
	assert.Equal(t, "and now live", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchSessionFeed_AutoFallsBackToStore: no coordinator endpoint anywhere
// → the S0 store tail feeds, transparently.
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
	f := newFakeConsumerServer()
	f.setRuns(runInfoFor(harp, "run-1"))
	startFakeCoordinator(t, home, "proj", f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed, err := WatchSessionFeed(ctx, SessionFeedRequest{Harp: harp, Source: FeedSourceStore})
	require.NoError(t, err)
	assert.Equal(t, "store", feed.Source)
	assert.Equal(t, "stored question", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchSessionFeed_ForcedLiveErrorsWhenNotLive: --source live is a hard
// error when no coordinator holds the harp — no silent store fallback.
func TestWatchSessionFeed_ForcedLiveErrorsWhenNotLive(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, true)

	_, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceLive})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no live tap")
}

// TestWatchSessionFeed_LiveNotHoldingHarpFallsBack: a reachable coordinator
// that does NOT hold the harp (its roster names a different agent_id)
// answers not-live for THIS harp — auto moves on to the store.
func TestWatchSessionFeed_LiveNotHoldingHarpFallsBack(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, true)
	f := newFakeConsumerServer()
	f.setRuns(runInfoFor("some-other-child", "run-1")) // live, but for a different harp
	startFakeCoordinator(t, home, "proj", f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed, err := WatchSessionFeed(ctx, SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "store", feed.Source)
	assert.Equal(t, "stored question", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}

// TestWatchFeed_DiscoveryAcrossMultipleCoordinators: with two candidate
// coordinators on disk, the resolver tries each (most-recently-active
// first, internal/agentcoord/discover's policy) until one holds the harp.
func TestWatchFeed_DiscoveryAcrossMultipleCoordinators(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, false)

	other := newFakeConsumerServer() // holds nothing
	startFakeCoordinator(t, home, "proj-a", other)
	mine := newFakeConsumerServer()
	mine.setRuns(runInfoFor(harp, "run-1"))
	startFakeCoordinator(t, home, "proj-b", mine)

	feed, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)
	assert.Equal(t, "live", feed.Source)

	assistantMessage(t, mine, "m-1", "found you")
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

// TestAdaptConsumerFeed_ToolCallsAndBoundaries drives the normalization core
// through a live fake coordinator: a message, a tool call/result pair, and a
// turn boundary — item-lifecycle AgentEvents folding back onto whole
// SessionEntry values, feed-relative boundary indexes across turns, and an
// empty turn (no new material) emitting no boundary.
func TestAdaptConsumerFeed_ToolCallsAndBoundaries(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedFeedHarp(t, home, false)
	f := newFakeConsumerServer()
	f.setRuns(runInfoFor(harp, "run-1"))
	startFakeCoordinator(t, home, "proj", f)

	feed, err := WatchSessionFeed(context.Background(), SessionFeedRequest{Harp: harp, Source: FeedSourceAuto})
	require.NoError(t, err)

	assistantMessage(t, f, "m-1", "one")
	turnIdle(t, f)

	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallStarted{ToolCallStarted: &agentcoordpb.ToolCallStarted{
		ToolCallId: "tc-1", ToolName: "Bash",
	}}})
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallCompleted{ToolCallCompleted: &agentcoordpb.ToolCallCompleted{
		ToolCallId: "tc-1", ResultText: "ok",
	}}})
	turnIdle(t, f)
	turnIdle(t, f) // an empty turn (no new material since the last boundary): no event

	assert.Equal(t, "one", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
	b1 := nextFeedEvent(t, feed.Events).Event.GetEvent().(*pb.WatchEvent_Boundary).Boundary
	assert.Equal(t, int32(0), b1.GetFromIndex())
	assert.Equal(t, int32(1), b1.GetToIndex())

	toolUse := nextFeedEvent(t, feed.Events)
	use, ok := toolUse.Event.GetEvent().(*pb.WatchEvent_Entry)
	require.True(t, ok)
	assert.Equal(t, "tool_use", use.Entry.GetType())
	assert.Equal(t, "Bash", use.Entry.GetToolName())

	toolResult := nextFeedEvent(t, feed.Events)
	res, ok := toolResult.Event.GetEvent().(*pb.WatchEvent_Entry)
	require.True(t, ok)
	assert.Equal(t, "tool_result", res.Entry.GetType())
	assert.Equal(t, "ok", res.Entry.GetToolOutput())

	b2 := nextFeedEvent(t, feed.Events).Event.GetEvent().(*pb.WatchEvent_Boundary).Boundary
	assert.Equal(t, int32(1), b2.GetFromIndex())
	assert.Equal(t, int32(3), b2.GetToIndex())

	// The second, empty turn_idle must not produce a further event — assert
	// by pushing one more distinguishable entry and confirming it arrives
	// immediately next (nothing queued ahead of it).
	assistantMessage(t, f, "m-2", "three")
	assert.Equal(t, "three", feedEntryContent(t, nextFeedEvent(t, feed.Events)))
}
