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

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

// This file is the hermetic live-tap end-to-end harness: a REAL in-process
// coordinator whose child is a REAL in-process runner (livecoord) -> the
// coordinator's watchHub -> ConsumerService.WatchRuns -> the live path of
// operations.WatchSessionFeed, discovery and all -> the in-process frontend
// port -> the real tui.SplitView -> the child's assistant entry rendered as a
// feed item in the pane.
//
// Placement: package tui_test (external), beside the split view it asserts on: a
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
// reaching a real notice in the real split view.

// splitW and splitH are the human's terminal the split view runs on here.
const splitW, splitH = 120, 30

// idleTerm is the session's own terminal: nothing runs on it, so it takes
// what the split view writes and never yields output.
type idleTerm struct{}

func (idleTerm) Write(p []byte) (int, error) { return len(p), nil }
func (idleTerm) Read([]byte) (int, error)    { select {} }
func (idleTerm) Resize(uint16, uint16) error { return nil }

// humanScreen is the human's terminal: the split view's output replayed on
// a reference emulator, every write waking a wait. bubbletea writes to a
// non-tty as to a cooked terminal, relying on ONLCR; the reference is raw,
// so the mapping is done here.
type humanScreen struct {
	mu   sync.Mutex
	ref  *termemu.XVT
	wake chan struct{}
}

func newHumanScreen(t *testing.T) *humanScreen {
	h := &humanScreen{ref: termemu.NewXVT(splitW, splitH, io.Discard, termemu.Callbacks{}), wake: make(chan struct{})}
	t.Cleanup(func() { _ = h.ref.Close() })
	return h
}

func (h *humanScreen) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.ref.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n")))
	close(h.wake)
	h.wake = make(chan struct{})
	return len(p), nil
}

// waitFor blocks until the screen shows text. 60s: a REAL dial-home and
// StartRun round trip over a real loopback listener, visibly slower under a
// full `-race ./...` run than alone.
func (h *humanScreen) waitFor(t *testing.T, what string, texts ...string) {
	t.Helper()
	bound := time.After(60 * time.Second)
	for {
		h.mu.Lock()
		screen, wake := strings.Join(h.ref.Snapshot().Text(), "\n"), h.wake
		h.mu.Unlock()
		shown := true
		for _, text := range texts {
			shown = shown && strings.Contains(screen, text)
		}
		if shown {
			return
		}
		select {
		case <-wake:
		case <-bound:
			t.Fatalf("timed out waiting for %s; the screen:\n%s", what, screen)
		}
	}
}

// startLiveSplit starts the split view over port as the session self, and
// returns the human's input and screen.
func startLiveSplit(t *testing.T, port frontend.Port, self string) (*io.PipeWriter, *humanScreen) {
	t.Helper()
	prefix, err := tui.ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	pr, pw := io.Pipe()
	screen := newHumanScreen(t)
	v, err := tui.StartSplit(context.Background(), tui.SplitOptions{
		Port: port, Self: self, Term: idleTerm{}, Prefix: prefix, In: pr, Out: screen, WindowSize: [2]int{splitW, splitH},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		v.Close()
		_ = pw.Close()
	})
	return pw, screen
}

// TestLiveTap_ChildItemsReachTheSplitView is the hermetic end-to-end proof:
// a live child's real event stream reaches the real split view as a feed
// item, through the coordinator's own Serve() endpoint and no stand-in.
func TestLiveTap_ChildItemsReachTheSplitView(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	s := livecoord.New(t, projectDir, 0)
	child := s.Delegate(t, "hello from the coordinator")

	// The split view's own port over the live session: its tree lists the
	// child, and selecting it resolves the live tap.
	port := operations.NewFrontendPort(operations.FrontendDeps{
		Coord: s.Coord, Engines: engines.Registry(), WorkDir: projectDir, Self: livecoord.OwnerHarp,
	})
	in, screen := startLiveSplit(t, port, livecoord.OwnerHarp)
	screen.waitFor(t, "the child in the tree", child)
	_, err := in.Write([]byte("\x1dj"))
	require.NoError(t, err)
	screen.waitFor(t, "the child's feed resolved live (not the store fallback)", "feed: "+child, "· live")

	_, err = port.Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: child, Body: "live words"})
	require.NoError(t, err)
	screen.waitFor(t, "the child's real assistant entry as a feed item", "asst  < echo: ", "live words")
}
