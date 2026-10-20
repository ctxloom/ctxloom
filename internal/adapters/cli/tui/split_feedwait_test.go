package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// panelText is the split's left panel, a row each, styles stripped.
func panelText(h *splitHarness) []string {
	var out []string
	for _, r := range h.screen() {
		out = append(out, strings.TrimRight(strings.SplitN(r, "│", 2)[0], " "))
	}
	return out
}

// panelLinesWith are the panel's rows that say text.
func panelLinesWith(h *splitHarness, text string) []string {
	var out []string
	for _, l := range panelText(h) {
		if strings.Contains(l, text) {
			out = append(out, l)
		}
	}
	return out
}

// A child with nothing captured yet is a feed that has not started, not a
// failure: the panel says nothing, the pane says it waits, and the open is
// retried until the transcript lands and its turns show.
func TestSplitChild_NothingCapturedYetWaitsForTheFeed(t *testing.T) {
	h, port := childHarness(t)
	notYet := fmt.Errorf("session %q has %w", "brave-heron", frontend.ErrNothingCaptured)
	port.feedErrs = map[string][]error{"brave-heron": {notYet, notYet}}
	h.typeBytes("\x1d")
	h.keys("j")
	require.Equal(t, []string{"brave-heron"}, port.watchedHarps())
	assert.Empty(t, h.s.noteLine(), "no feed note in the panel")
	assert.Empty(t, panelLinesWith(h, "brave-heron:"))
	assert.Contains(t, strings.Join(paneText(h), "\n"), feedWaitingNote)

	h.advance(2*time.Second - time.Millisecond)
	assert.Len(t, port.watchedHarps(), 1, "not retried before its interval")
	h.advance(time.Millisecond)
	assert.Equal(t, []string{"brave-heron", "brave-heron"}, port.watchedHarps(), "retried every two seconds")
	assert.Empty(t, h.s.noteLine())
	h.advance(feedRetryEvery)
	require.Equal(t, []string{"brave-heron", "brave-heron", "brave-heron"}, port.watchedHarps())
	assert.NotContains(t, strings.Join(paneText(h), "\n"), feedWaitingNote, "an open feed is no longer waiting")

	h.send(feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "first captured turn")})
	pane := strings.Join(paneText(h), "\n")
	assert.Contains(t, pane, "asst  < first captured turn")
	assert.NotContains(t, pane, feedWaitingNote)
	h.advance(feedRetryEvery)
	assert.Len(t, port.watchedHarps(), 3, "an open feed is not retried")
}

// A retry due after the pane moved on opens nothing — even when it has come
// back to the same child, whose own open keeps its own retry.
func TestSplitChild_FeedRetryForAHarpNoLongerViewed(t *testing.T) {
	h, port := childHarness(t)
	port.feedErrs = map[string][]error{"brave-heron": {frontend.ErrNothingCaptured, frontend.ErrNothingCaptured}}
	h.typeBytes("\x1d")
	h.keys("j", "k")
	h.advance(feedRetryEvery / 2)
	h.keys("j")
	require.Equal(t, []string{"brave-heron", "brave-heron"}, port.watchedHarps())
	h.advance(feedRetryEvery / 2)
	assert.Len(t, port.watchedHarps(), 2, "the first open's retry is dropped")
	h.advance(feedRetryEvery / 2)
	assert.Len(t, port.watchedHarps(), 3, "the second open's retry stands")
}

// A genuine feed failure is the panel's note, on one line cut to the
// panel's width rather than a wrapped paragraph.
func TestSplitChild_FeedErrorIsOneTruncatedLine(t *testing.T) {
	h, port := childHarness(t)
	long := errors.New(strings.Repeat("the coordinator refused the watch ", 6))
	port.feedErrs = map[string][]error{"brave-heron": {long}}
	h.typeBytes("\x1d")
	h.keys("j")
	hits := panelLinesWith(h, "feed brave-heron:")
	require.Len(t, hits, 1, "%q", panelText(h))
	assert.True(t, strings.HasSuffix(hits[0], "…"), "cut short, and says so: %q", hits[0])
	assert.Equal(t, rosterPaneWidth, ansi.StringWidth(hits[0]), "cut to the panel's width")
	assert.Empty(t, panelLinesWith(h, "watch"), "nothing of the error wraps onto a line of its own: %q", panelText(h))
	h.advance(feedRetryEvery)
	assert.Equal(t, []string{"brave-heron"}, port.watchedHarps(), "a genuine failure is not retried")

	h.send(feedClosedMsg{harp: "brave-heron", err: long})
	hits = panelLinesWith(h, "feed ended: the")
	require.Len(t, hits, 1, "a feed that ends in failure says so on one line too")
	assert.True(t, strings.HasSuffix(hits[0], "…"))
	assert.Empty(t, panelLinesWith(h, "watch"))
}
