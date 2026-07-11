package coord_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/cli/tui"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// This file is the gap-detection sibling of livetap_test.go's F1 harness:
// it proves that a real seq discontinuity on the wire — the shape
// consumer.go's watchHub.broadcast produces when a slow subscriber's
// bounded ring drops an event (PART 2) — really does turn into a real,
// visible notice in the real tui.Overlay (PART 1's gap detection, wired all
// the way through). It deliberately does NOT try to provoke an actual
// buffer overflow by racing a real StartRun child's event volume against
// gRPC flow control: that mechanism is proven directly and deterministically
// by consumer_test.go's TestWatchHub_Broadcast_TerminalEvictsOnFullBuffer...
// and friends, and basing a hermetic, always-green test suite on winning a
// real network-timing race would trade a fast, certain proof for a slow,
// flaky one that proves the same thing less clearly. Instead this test
// stands up a minimal fake ConsumerService (the same hermetic-double idiom
// internal/operations/sessionfeed_test.go's fakeConsumerServer uses,
// re-derived here — that type is unexported to package operations) and
// hands it a seq that skips two numbers: exactly the wire shape a real hub
// drop produces. What is proven here is the CONSUMER side of the contract
// this package cannot otherwise prove alone (operations/tui import coord;
// coord cannot import them back — see livetap_test.go's header comment).

// gapFakeConsumer is a hermetic double for agentcoord.v1.ConsumerService,
// scoped down to exactly what WatchSessionFeed's live-tap discovery needs:
// a roster naming one harp/run, and a WatchRuns stream this test pushes
// hand-crafted AgentEvents onto (including a deliberate seq skip).
type gapFakeConsumer struct {
	agentcoordpb.UnimplementedConsumerServiceServer

	mu   sync.Mutex
	runs []*agentcoordpb.ListRunsResult_RunInfo
	subs map[chan *agentcoordpb.AgentEvent]struct{}
}

func newGapFakeConsumer(harp, runID string) *gapFakeConsumer {
	return &gapFakeConsumer{
		runs: []*agentcoordpb.ListRunsResult_RunInfo{{
			RunId: runID,
			Agent: &agentcoordpb.AgentIdentity{AgentId: harp},
		}},
		subs: map[chan *agentcoordpb.AgentEvent]struct{}{},
	}
}

func (f *gapFakeConsumer) ListRuns(context.Context, *agentcoordpb.ListRunsRequest) (*agentcoordpb.ListRunsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &agentcoordpb.ListRunsResult{Runs: f.runs}, nil
}

func (f *gapFakeConsumer) WatchRuns(_ *agentcoordpb.WatchRunsRequest, stream grpc.ServerStreamingServer[agentcoordpb.WatchEvent]) error {
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

// push broadcasts ev to every live WatchRuns subscriber, waiting for one to
// attach first (mirrors sessionfeed_test.go's fakeConsumerServer.push).
func (f *gapFakeConsumer) push(t *testing.T, ev *agentcoordpb.AgentEvent) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.subs) > 0
	}, feedWait, 5*time.Millisecond, "no WatchRuns subscriber attached")
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		ch <- ev
	}
}

const feedWait = 5 * time.Second

// startGapFakeCoordinator serves f over a real loopback gRPC listener and
// writes the endpoint.json operations' internal/agentcoord/discover looks
// for (mirrors sessionfeed_test.go's startFakeCoordinator, re-derived here:
// that helper is unexported to package operations).
func startGapFakeCoordinator(t *testing.T, home, projectKey string, f *gapFakeConsumer) {
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

// seedGapHarp mints a bare index entry (no transcript association — this
// test only exercises the live path, never store scrollback).
func seedGapHarp(t *testing.T, projectDir string) string {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	return entry.HarpName
}

// toolCallEvent is one self-contained ToolCallStarted AgentEvent at seq —
// flushes to exactly one feed entry, so the notice's position relative to
// it is unambiguous (the same shape sessionfeed_test.go's own seq-gap table
// test uses).
func toolCallEvent(seq uint64) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{
		Seq: seq,
		Payload: &agentcoordpb.AgentEvent_ToolCallStarted{ToolCallStarted: &agentcoordpb.ToolCallStarted{
			ToolCallId: fmt.Sprintf("tc-%d", seq),
			ToolName:   fmt.Sprintf("seq-%d", seq),
		}},
	}
}

// TestLiveTap_GapNoticeReachesTheOverlay proves a real seq gap on the wire
// renders as a real, visible notice in the real tui.Overlay: entry (seq 1)
// -> [seq 4 arrives, skipping 2 and 3] -> a gap notice -> entry (seq 4) ->
// clean end on RunCompleted. This makes obsolete the note livetap_test.go
// used to carry ("operations.SessionFeedEvent.Gap is never set anywhere on
// the live path any more... a vestige of the retired agentbus tap").
func TestLiveTap_GapNoticeReachesTheOverlay(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	harp := seedGapHarp(t, projectDir)

	f := newGapFakeConsumer(harp, "run-1")
	startGapFakeCoordinator(t, home, "proj", f)

	feed, err := operations.WatchSessionFeed(context.Background(), operations.SessionFeedRequest{Harp: harp, Source: operations.FeedSourceAuto})
	require.NoError(t, err)
	require.Equal(t, "live", feed.Source)

	src := tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) {
			return []tui.RosterRow{{Harp: harp, State: "live"}}, nil
		},
		Watch: func(context.Context, string) (*tui.Feed, error) {
			return &tui.Feed{Source: feed.Source, Events: feed.Events, Errs: feed.Errs, Cancel: func() {}}, nil
		},
		Now: time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ov := tui.NewOverlay(ctx, src, 0x1d)
	pr, pw := io.Pipe()
	defer pw.Close()
	var tty syncBuf
	done := make(chan error, 1)
	go func() { done <- ov.Run(pr, &tty, tuiGeo()) }()

	waitForLiveTap(t, "the feed to resolve live", func() bool {
		return strings.Contains(tty.String(), "· live")
	})

	f.push(t, toolCallEvent(1))
	waitForLiveTap(t, "the first tool_use entry to render", func() bool {
		return strings.Contains(tty.String(), "seq-1")
	})

	// Skip 2 and 3: a real hub drop, on the wire, looks exactly like this.
	f.push(t, toolCallEvent(4))
	waitForLiveTap(t, "a gap notice for the 2 skipped events to render", func() bool {
		return strings.Contains(tty.String(), "2 live events dropped")
	})
	waitForLiveTap(t, "the entry after the gap to render", func() bool {
		return strings.Contains(tty.String(), "seq-4")
	})

	f.push(t, &agentcoordpb.AgentEvent{Seq: 5, Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}})

	_, err = pw.Write([]byte("q"))
	require.NoError(t, err)
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(3 * time.Second):
		t.Fatal("overlay did not quit")
	}
}
