package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/termui"
)

// fakeSources records watches/cancels and lets tests push feed events.
type fakeSources struct {
	rows      []RosterRow
	watched   []string
	events    map[string]chan operations.SessionFeedEvent
	cancelled map[string]int
	exportDir string
}

func newFakeSources(dir string, rows ...RosterRow) *fakeSources {
	return &fakeSources{
		rows:      rows,
		events:    map[string]chan operations.SessionFeedEvent{},
		cancelled: map[string]int{},
		exportDir: dir,
	}
}

func (f *fakeSources) sources() Sources {
	return Sources{
		Roster: func(context.Context) ([]RosterRow, error) { return f.rows, nil },
		Watch: func(_ context.Context, harp string) (*Feed, error) {
			f.watched = append(f.watched, harp)
			ch := make(chan operations.SessionFeedEvent, 16)
			f.events[harp] = ch
			errs := make(chan error, 1)
			return &Feed{
				Source: "live",
				Events: ch,
				Errs:   errs,
				Cancel: func() { f.cancelled[harp]++ },
			}, nil
		},
		ExportDir: func(string) (string, error) { return f.exportDir, nil },
		Now:       func() time.Time { return time.Date(2026, 7, 7, 10, 15, 0, 0, time.UTC) },
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+]":
		return tea.KeyMsg{Type: tea.KeyCtrlCloseBracket}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func entryEv(role, content string) operations.SessionFeedEvent {
	return operations.SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{
		Type: role, Content: content, TimestampUnix: 1700000000,
	}}}}
}

// step applies msg and returns the updated model + cmd.
func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	mm, cmd := m.Update(msg)
	return mm.(Model), cmd
}

// openSelected pumps the roster + feed-open handshake for the initial model.
func openSelected(t *testing.T, m Model, f *fakeSources) Model {
	t.Helper()
	m, cmd := step(t, m, rosterMsg{rows: f.rows})
	require.NotNil(t, cmd, "first roster must auto-open the selection's feed")
	m, cmd = step(t, m, cmd())
	require.NotNil(t, cmd, "feedOpenedMsg must arm the event wait")
	return m
}

// pushEntry delivers one event through the armed wait cycle.
func pushEntry(t *testing.T, m Model, f *fakeSources, ev operations.SessionFeedEvent) Model {
	t.Helper()
	f.events[m.feedHarp] <- ev
	msg := waitEventCmd(m.feedHarp, m.feed)()
	m, _ = step(t, m, msg)
	return m
}

func testGeo() termui.OverlayGeometry {
	return termui.OverlayGeometry{Cols: 100, Rows: 30, PanelRows: 10}
}

func newTestModel(f *fakeSources, copyTo *bytes.Buffer) Model {
	return NewModel(context.Background(), f.sources(), testGeo(), 0x1d, copyTo)
}

func TestModel_RosterAutoSelectsAndWatches(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "perky-same-chevy", Engine: "claude-code", State: "live", Self: true},
		RosterRow{Harp: "swift-elm-fox", Agent: "dev", State: "executing", Depth: 1},
	)
	m := openSelected(t, newTestModel(f, nil), f)

	assert.Equal(t, []string{"perky-same-chevy"}, f.watched)
	assert.Equal(t, "live", m.feedSource)
	view := m.View()
	assert.Contains(t, view, "feed: perky-same-chevy")
	assert.Contains(t, view, "swift-elm-fox·dev")
	assert.Contains(t, view, "▼ follow")
}

func TestModel_FeedEventsAppendAndFollowTail(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)

	m = pushEntry(t, m, f, entryEv("user", "refactor the parser"))
	m = pushEntry(t, m, f, entryEv("assistant", "done; deferred: lexer tests"))

	require.Len(t, m.items, 2)
	assert.Equal(t, 1, m.cursor, "follow mode tails the newest entry")
	view := m.View()
	assert.Contains(t, view, "user  > refactor the parser")
	assert.Contains(t, view, "asst  < done; deferred: lexer tests")
}

func TestModel_RosterNavigationSwitchesFeed(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "h1", State: "live"},
		RosterRow{Harp: "h2", State: "executing"},
	)
	m := openSelected(t, newTestModel(f, nil), f)

	m, cmd := step(t, m, keyMsg("j"))
	require.NotNil(t, cmd, "selecting another harp opens its feed")
	assert.Equal(t, 1, f.cancelled["h1"], "the previous watch is released")
	m, _ = step(t, m, cmd())
	assert.Equal(t, []string{"h1", "h2"}, f.watched)
	assert.Equal(t, "h2", m.feedHarp)
}

func TestModel_StaleFeedOpenIsReleased(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "h1", State: "live"},
		RosterRow{Harp: "h2", State: "live"},
	)
	m, cmd := step(t, newTestModel(f, nil), rosterMsg{rows: f.rows})
	openH1 := cmd() // h1's feedOpenedMsg, not yet applied

	m, cmd = step(t, m, keyMsg("j")) // selection moves to h2 first
	m, _ = step(t, m, cmd())
	m, _ = step(t, m, openH1) // the stale h1 open arrives late
	assert.Equal(t, 1, f.cancelled["h1"], "a stale open is cancelled, not adopted")
	assert.Equal(t, "h2", m.feedHarp)
}

func TestModel_FirstKeyFEntersFullScreen(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)

	m, cmd := step(t, m, keyMsg("f"))
	assert.True(t, m.full, "prefix-then-f = full screen")
	require.NotNil(t, cmd, "must return the EnterAltScreen command")
	assert.False(t, m.follow == false, "follow untouched by the presentation switch")
	assert.Equal(t, m.geo.Rows, m.totalHeight(), "full screen uses the whole terminal")
}

func TestModel_LaterFTogglesFollow(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m, _ = step(t, m, keyMsg("j")) // consume the first-key chord window

	require.True(t, m.follow)
	m, _ = step(t, m, keyMsg("f"))
	assert.False(t, m.follow, "after the first key, f toggles follow")
	assert.False(t, m.full)
	m, _ = step(t, m, keyMsg("f"))
	assert.True(t, m.follow)
}

func TestModel_ExpandCollapse(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, operations.SessionFeedEvent{Event: &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{
		Type: "tool_result", ToolName: "Bash", ToolOutput: "line1\nline2\nline3",
	}}}})

	assert.Contains(t, m.View(), "ok (3 lines)")
	assert.NotContains(t, m.View(), "line2")

	m, _ = step(t, m, keyMsg("x"))
	assert.Contains(t, m.View(), "line2", "x expands the tool detail")

	m, _ = step(t, m, keyMsg("x"))
	assert.NotContains(t, m.View(), "line2", "x collapses it again")
}

func TestModel_ScrollbackEnds(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	for i := 0; i < 30; i++ {
		m = pushEntry(t, m, f, entryEv("assistant", "entry"))
	}

	m, _ = step(t, m, keyMsg("g"))
	assert.False(t, m.follow, "g jumps to the top and leaves the tail")
	assert.Equal(t, 0, m.cursor)
	assert.Equal(t, 0, m.vp.YOffset)

	m, _ = step(t, m, keyMsg("G"))
	assert.True(t, m.follow, "G returns to the tail and re-follows")
	assert.Equal(t, 29, m.cursor)
}

func TestModel_QuitKeysCancelFeed(t *testing.T) {
	for _, key := range []string{"q", "ctrl+]"} {
		f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
		m := openSelected(t, newTestModel(f, nil), f)
		m, _ = step(t, m, keyMsg("j")) // past the chord window

		_, cmd := step(t, m, keyMsg(key))
		require.NotNil(t, cmd, key)
		assert.IsType(t, tea.QuitMsg{}, cmd(), "%s backs out to the engine", key)
		assert.Equal(t, 1, f.cancelled["h1"], "%s releases the watch", key)
	}
}

func TestModel_ExportWritesTranscriptFile(t *testing.T) {
	dir := t.TempDir()
	f := newFakeSources(dir, RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, entryEv("user", "hello"))
	m = pushEntry(t, m, f, entryEv("assistant", "world"))

	m, _ = step(t, m, keyMsg("s"))
	require.Contains(t, m.status, "saved ")
	path := strings.TrimPrefix(m.status, "saved ")
	assert.Equal(t, filepath.Join(dir, "transcript-h1-20260707T101500.txt"), path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(transcriptText(m.items, 100)), string(data))

	m, _ = step(t, m, keyMsg("S"))
	require.Contains(t, m.status, ".ndjson")
	ndPath := strings.TrimPrefix(m.status, "saved ")
	nd, err := os.ReadFile(ndPath)
	require.NoError(t, err)
	assert.Contains(t, string(nd), `"type":"user"`)
	assert.Contains(t, string(nd), `"content":"hello"`)
}

func TestModel_CopyEmitsOSC52WithFileFallback(t *testing.T) {
	var copyBuf bytes.Buffer
	dir := t.TempDir()
	f := newFakeSources(dir, RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, &copyBuf), f)
	m = pushEntry(t, m, f, entryEv("user", "copy me"))

	m, _ = step(t, m, keyMsg("y"))
	assert.True(t, strings.HasPrefix(copyBuf.String(), "\x1b]52;c;"),
		"y emits an OSC 52 clipboard write")
	assert.Contains(t, m.status, "copied (OSC 52)")
	assert.Contains(t, m.status, "in case the terminal ignored it",
		"the fallback file is named for terminals that ignore OSC 52")
}

func TestModel_GapRendersNotice(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, operations.SessionFeedEvent{Gap: 7})
	assert.Contains(t, m.View(), "7 live events dropped")
}

func TestModel_RosterRefreshKeepsSelection(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "h1", State: "live"},
		RosterRow{Harp: "h2", State: "live"},
	)
	m := openSelected(t, newTestModel(f, nil), f)
	m, cmd := step(t, m, keyMsg("j"))
	m, _ = step(t, m, cmd())
	require.Equal(t, "h2", m.feedHarp)

	// A refresh with a new row prepended must not steal the selection.
	refreshed := []RosterRow{{Harp: "h0", State: "live"}, {Harp: "h1", State: "live"}, {Harp: "h2", State: "live"}}
	m, cmd = step(t, m, rosterMsg{rows: refreshed})
	assert.Nil(t, cmd, "no re-open when the selected harp survives the refresh")
	assert.Equal(t, "h2", m.rows[m.sel].Harp)
}

func TestModel_FeedClosedReportsEnd(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	close(f.events["h1"])
	msg := waitEventCmd("h1", m.feed)()
	m, _ = step(t, m, msg)
	assert.Contains(t, m.status, "feed ended")
}
