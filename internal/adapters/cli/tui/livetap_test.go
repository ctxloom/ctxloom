package tui_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

// This file is the hermetic live-tap end-to-end harness: a REAL in-process
// coordinator whose child is a REAL in-process runner (livecoord) -> the
// coordinator's watchHub -> ConsumerService.WatchRuns -> the live path of
// operations.WatchSessionFeed, discovery and all -> the real tui.Overlay ->
// the child's assistant entry rendered as an overlay item.
//
// Placement: package tui_test (external), beside the overlay it asserts on: a
// test that needs a UI to assert is that UI adapter's test, and the frontend
// layering rule (core-and-shared-import-no-frontend) keeps UI imports out of
// core.
//
// The live tap is forward-only fan-out: a broadcast with no subscriber yet is
// gone. So the feed is opened first and the turn it must show — a steer —
// comes after.
//
// Gap markers (feed.go — itemsFromFeedEvent's Gap>0 branch) are not
// provoked here; livetap_gap_test.go proves a real seq gap on the wire
// reaching a real notice in the real overlay.

// tuiGeo is a plain small overlay geometry, sized like model_test.go's
// testGeo() (unreachable from this external package).
func tuiGeo() termui.OverlayGeometry {
	return termui.OverlayGeometry{Cols: 100, Rows: 30, PanelRows: 10}
}

// syncBuf is a goroutine-safe io.Writer: the overlay's tty writer runs on its
// own goroutine.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForLiveTap(t *testing.T, what string, cond func() bool) {
	t.Helper()
	// 60s: a REAL dial-home and StartRun round trip over a real loopback
	// listener, visibly slower under a full `-race ./...` run than alone.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestLiveTap_ChildItemsReachTheOverlay is the hermetic end-to-end proof:
// a live child's real event stream reaches the real overlay as a feed item,
// through the coordinator's own Serve() endpoint and no stand-in.
func TestLiveTap_ChildItemsReachTheOverlay(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	s := livecoord.New(t, projectDir, 0)
	child := s.Delegate(t, "hello from the coordinator")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed, err := operations.WatchSessionFeed(ctx, engines.Registry(), operations.SessionFeedRequest{Harp: child})
	require.NoError(t, err)
	require.Equal(t, "live", feed.Source, "the coordinator holds this child; the tap must be live, not the store fallback")

	src := tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) {
			return []tui.RosterRow{{Harp: child, State: "live"}}, nil
		},
		Watch: func(context.Context, string) (*tui.Feed, error) {
			return &tui.Feed{Source: feed.Source, Events: feed.Events, Errs: feed.Errs, Cancel: func() {}}, nil
		},
	}
	ov := tui.NewOverlay(ctx, src, 0x1d, termui.OverlayStart{})
	pr, pw := io.Pipe()
	defer pw.Close()
	var tty syncBuf
	done := make(chan error, 1)
	go func() { done <- ov.Run(pr, &tty, tuiGeo()) }()
	waitForLiveTap(t, "the feed to resolve live (not the store fallback)", func() bool {
		return strings.Contains(tty.String(), "· live")
	})

	_, err = s.Coord.Control(ctx, coord.ControlInitiator{Kind: coord.InitiatorHuman}, coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: child, Body: "live words"})
	require.NoError(t, err)
	waitForLiveTap(t, "the child's real assistant entry to render as an overlay item", func() bool {
		return strings.Contains(tty.String(), "asst  < echo: ") && strings.Contains(tty.String(), "live words")
	})

	_, err = pw.Write([]byte("q"))
	require.NoError(t, err)
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(3 * time.Second):
		t.Fatal("overlay did not quit")
	}
}
