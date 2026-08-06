package tui

import (
	"bytes"
	"context"
	"sync"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/termui"
)

// fakeSources records watches/cancels/injects and lets tests push feed
// events and script the inject outcome.
type fakeSources struct {
	// mu guards watched, which the overlay tests read from the test goroutine
	// while a real tea.Program appends to it from its own.
	mu        sync.Mutex
	rows      []RosterRow
	watched   []string
	events    map[string]chan operations.SessionFeedEvent
	cancelled map[string]int
	exportDir string
	// exportDirErr, when set, makes ExportDir fail — the fallback-write
	// failure path for the copy sink.
	exportDirErr error

	injected   [][2]string // {harp, text} per inject call
	injectMode string      // "" defaults to DeliveryQueued
	injectErr  error
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
			f.mu.Lock()
			f.watched = append(f.watched, harp)
			f.mu.Unlock()
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
		ExportDir: func(string) (string, error) {
			// Faithful to the production wiring (cli.terminalUISources), whose
			// ExportDir is `return dir, os.MkdirAll(dir, 0o755)` — a NON-EMPTY
			// path alongside a non-nil error. The fake must hand back the same
			// shape, or it silently exempts every consumer from checking the
			// error before the path.
			return f.exportDir, f.exportDirErr
		},
		Now: func() time.Time { return time.Date(2026, 7, 7, 10, 15, 0, 0, time.UTC) },
		Inject: func(harp, text string) (string, error) {
			f.injected = append(f.injected, [2]string{harp, text})
			if f.injectErr != nil {
				return "", f.injectErr
			}
			if f.injectMode == "" {
				return coord.DeliveryQueued, nil
			}
			return f.injectMode, nil
		},
	}
}

// keyMsg builds the bubbletea v2 key press a terminal would deliver for s.
// v2 has no KeyRunes/KeySpace types: printable input is carried in Key.Text
// and the key itself in Key.Code, so the default arm sets both — Text is what
// the inject line appends, Code is what Key.String() matches bindings on.
func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+]":
		return tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
}

// watchedHarps copies the watch log under the lock, for tests that read it
// while a Program is running.
func (f *fakeSources) watchedHarps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.watched...)
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

// copyTo is an io.Writer (not *bytes.Buffer) so that passing nil yields a
// genuinely nil interface — a typed-nil *bytes.Buffer passes the model's
// `m.copyTo != nil` guard and panics on Write.
func newTestModel(f *fakeSources, copyTo io.Writer) Model {
	return NewModel(context.Background(), f.sources(), testGeo(), 0x1d, copyTo)
}

func TestModel_RosterAutoSelectsAndWatches(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "perky-same-chevy", Engine: "claude-code", State: "live"},
		RosterRow{Harp: "swift-elm-fox", Agent: "dev", State: "executing", Depth: 1},
	)
	m := openSelected(t, newTestModel(f, nil), f)

	assert.Equal(t, []string{"perky-same-chevy"}, f.watched)
	assert.Equal(t, "live", m.feedSource)
	view := m.render()
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
	view := m.render()
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

// Every field openFeed resets has to be visible in the model Update RETURNS,
// not just in the receiver it mutated. If the returned value is the pre-call
// copy, the feed switch is silently half-applied: feedHarp still names the old
// harp, so the incoming feedOpenedMsg is judged stale and cancelled, and the
// new agent's feed never opens at all.
func TestModel_SelectionMoveReturnsTheModelOpenFeedReset(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "h1", State: "live"},
		RosterRow{Harp: "h2", State: "live"},
	)
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, entryEv("user", "old feed content"))
	m.expanded[0] = true
	m.follow = false
	m.cursor = 0
	require.NotEmpty(t, m.items)

	for _, key := range []string{"j", "k"} {
		before := m.feedHarp
		m, _ = step(t, m, keyMsg(key))
		require.NotEqual(t, before, m.feedHarp, "key=%s moved the selection", key)
		assert.Empty(t, m.items, "key=%s: the returned model carries the reset item list", key)
		assert.Empty(t, m.expanded, "key=%s: and the reset expansion set", key)
		assert.Empty(t, m.feedSource, "key=%s: and the cleared source", key)
		assert.Equal(t, 0, m.cursor, "key=%s", key)
		assert.True(t, m.follow, "key=%s: a fresh feed tails", key)
		assert.Nil(t, m.feed, "key=%s: the previous feed handle is gone", key)
	}
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
	// v2 splits what v1's EnterAltScreen command did into two halves, and both
	// have to happen or full screen is broken in a different way each time:
	// the View carries the alt screen, and a WindowSizeMsg grows the renderer
	// from the quick panel's height to the whole terminal. Without the resize
	// the alt screen paints into PanelRows rows.
	assert.True(t, m.View().AltScreen, "full screen renders on the alt screen")
	require.NotNil(t, cmd, "full screen must resize the renderer to the terminal")
	assert.Equal(t, tea.WindowSizeMsg{Width: m.geo.Cols, Height: m.geo.Rows}, cmd(),
		"the resize must carry the whole terminal, not the panel")
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

	assert.Contains(t, m.render(), "ok (3 lines)")
	assert.NotContains(t, m.render(), "line2")

	m, _ = step(t, m, keyMsg("x"))
	assert.Contains(t, m.render(), "line2", "x expands the tool detail")

	m, _ = step(t, m, keyMsg("x"))
	assert.NotContains(t, m.render(), "line2", "x collapses it again")
}

// Model's roster, feed and inject fields are NOT three independent sub-models:
// one focus field decides which of the first two a movement key drives, the
// feed's own header is rendered from the ROSTER's selected row, and the inject
// target is latched from the feed. Splitting the struct along those groups has
// to carry these couplings across, so they are pinned here rather than left to
// be discovered by whoever tries.
func TestModel_PaneStateIsCoupledNotThreeIndependentSubModels(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "h1", Agent: "developer", Engine: "claude-code", State: "live"},
		RosterRow{Harp: "h2", Agent: "finder", State: "executing"},
	)
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, entryEv("user", "one"))
	m = pushEntry(t, m, f, entryEv("assistant", "two"))

	// The feed pane's title is built from the roster's selected row.
	assert.Contains(t, m.feedTitle(), "h1")
	assert.Contains(t, m.feedTitle(), "developer·claude-code",
		"the feed header reads the roster row, not the feed")

	// focus routes one key to two different panes.
	require.Equal(t, focusRoster, m.focus)
	selBefore := m.sel
	m, cmd := step(t, m, keyMsg("j"))
	require.NotNil(t, cmd)
	assert.Equal(t, selBefore+1, m.sel, "with the roster focused, j moves the roster")
	m, _ = step(t, m, cmd())
	m = pushEntry(t, m, f, entryEv("user", "one"))
	m = pushEntry(t, m, f, entryEv("assistant", "two"))
	m, _ = step(t, m, keyMsg("enter"))
	require.Equal(t, focusFeed, m.focus)
	selBefore, cursorBefore := m.sel, m.cursor
	m, _ = step(t, m, keyMsg("k"))
	assert.Equal(t, selBefore, m.sel, "with the feed focused, the roster does not move")
	assert.Equal(t, cursorBefore-1, m.cursor, "the feed cursor does")

	// The inject target is latched from the feed, which was latched from the
	// roster: all three panes in one flow.
	m, _ = step(t, m, keyMsg("i"))
	require.True(t, m.injecting)
	assert.Equal(t, m.feedHarp, m.injectHarp)
	assert.Equal(t, m.rows[m.sel].Harp, m.injectHarp)
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
	assert.Equal(t, 0, m.vp.YOffset())

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

// A feed carrying only viewer chrome (gap notices) renders to zero bytes.
// Exporting it must fail loudly rather than report "saved <path>" for an
// empty file.
func TestModel_ExportAllNoticeFeedFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	f := newFakeSources(dir, RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, operations.SessionFeedEvent{Gap: 7})
	require.NotEmpty(t, m.items, "the notice is a real feed item, so the len==0 guard does not fire")

	for _, key := range []string{"s", "S"} {
		m, _ = step(t, m, keyMsg(key))
		assert.NotContains(t, m.status, "saved ", "key=%s: nothing was saved", key)
		assert.Contains(t, m.errMsg, "nothing to export", "key=%s", key)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no 0-byte transcript may be written")
}

// y on an all-notice feed used to emit OSC 52 with an empty payload — the
// clear-selection form — wiping the user's clipboard and reporting success.
func TestModel_CopyAllNoticeFeedRefusesInsteadOfClearingClipboard(t *testing.T) {
	var copyBuf bytes.Buffer
	dir := t.TempDir()
	f := newFakeSources(dir, RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, &copyBuf), f)
	m = pushEntry(t, m, f, operations.SessionFeedEvent{Gap: 7})
	require.NotEmpty(t, m.items)

	m, _ = step(t, m, keyMsg("y"))
	assert.Empty(t, copyBuf.String(),
		"an empty OSC 52 payload is the CLEAR-clipboard sequence; it must never be emitted")
	assert.NotContains(t, m.status, "copied", "nothing was copied")
	assert.Contains(t, m.status, "nothing to copy")
}

// The fallback file exists precisely because OSC 52 is unobservable. When it
// fails the user has no signal at all, so the failure must be reported.
func TestModel_CopyReportsFallbackAndSinkFailures(t *testing.T) {
	dir := t.TempDir()
	f := newFakeSources(dir, RosterRow{Harp: "h1", State: "live"})
	f.exportDirErr = fmt.Errorf("session dir unavailable")
	m := openSelected(t, newTestModel(f, failingWriter{}), f)
	m = pushEntry(t, m, f, entryEv("user", "copy me"))

	m, _ = step(t, m, keyMsg("y"))
	assert.Contains(t, m.errMsg, "session dir unavailable",
		"a failed fallback write must reach the user")
	assert.Contains(t, m.errMsg, "tty gone", "a failed OSC 52 write must reach the user")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("tty gone") }

func TestModel_GapRendersNotice(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, operations.SessionFeedEvent{Gap: 7})
	assert.Contains(t, m.render(), "7 live events dropped")
}

// The roster refreshes every rosterRefreshEvery, so anything the refresh
// clears has a lifetime of at most two seconds. "saved <path>" is the only
// place the user learns WHERE the export landed, so the background refresh
// must not own that line.
func TestModel_RosterRefreshDoesNotWipeAnActionOutcome(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, entryEv("user", "hello"))

	m, _ = step(t, m, keyMsg("s"))
	require.Contains(t, m.status, "saved ")

	m, _ = step(t, m, rosterMsg{rows: f.rows})
	assert.Contains(t, m.status, "saved ", "a background roster refresh must not eat the export path")
}

// A roster fetch that fails and then succeeds has recovered; the error line
// must go with it. Nothing else ever clears it, so before this it survived
// every later refresh for the life of the overlay.
func TestModel_SuccessfulRosterRefreshClearsTheEarlierRosterError(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)

	m, _ = step(t, m, rosterErrMsg{err: fmt.Errorf("dial coordinator: refused")})
	require.Contains(t, m.rosterErr, "dial coordinator: refused")
	require.Contains(t, m.hintNote(), "dial coordinator: refused", "and it is the line the user sees")

	m, _ = step(t, m, rosterMsg{rows: f.rows})
	assert.Empty(t, m.rosterErr, "a recovered roster fetch must retire its own error")
	assert.NotContains(t, m.hintNote(), "dial coordinator: refused")
}

// The hint bar shows ONE note, so a parked failure must not be able to hide a
// newer outcome forever: the action slot wins, the roster's own failure comes
// next, and the background note last.
func TestModel_HintNotePrecedence(t *testing.T) {
	m := Model{status: "saved /tmp/t.txt"}
	assert.Equal(t, "saved /tmp/t.txt", m.hintNote())

	m.rosterErr = "roster: refused"
	assert.Equal(t, "roster: refused", m.hintNote(), "a live roster failure outranks a background note")

	m.errMsg = "export: disk full"
	assert.Equal(t, "export: disk full", m.hintNote(), "the last action's failure outranks both")
}

// View prefers errMsg over status, so an action that succeeds while a stale
// error is still parked renders as the OLD failure — the success is
// invisible. Every action reports exactly one outcome.
func TestModel_ASucceedingActionRetiresTheEarlierFailure(t *testing.T) {
	dir := t.TempDir()
	f := newFakeSources(dir, RosterRow{Harp: "h1", State: "live"})
	f.exportDirErr = fmt.Errorf("session dir unavailable")
	m := openSelected(t, newTestModel(f, nil), f)
	m = pushEntry(t, m, f, entryEv("user", "hello"))

	m, _ = step(t, m, keyMsg("s"))
	require.Contains(t, m.errMsg, "session dir unavailable")

	f.exportDirErr = nil
	m, _ = step(t, m, keyMsg("s"))
	assert.Contains(t, m.status, "saved ")
	assert.Empty(t, m.errMsg, "the superseded failure must not outlive the success")
	assert.Contains(t, m.hintNote(), "saved ", "and the success is what the user actually sees")
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

func TestModel_InjectLineOpensTypesAndCancels(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "h1", State: "executing"},
		RosterRow{Harp: "h2", State: "idle"},
	)
	m := openSelected(t, newTestModel(f, nil), f)

	m, _ = step(t, m, keyMsg("i"))
	require.True(t, m.injecting)
	assert.Contains(t, m.render(), "inject → h1:", "the input line shows the explicit target harp")

	// While the line is open, navigation keys type into it, not the roster.
	m, cmd := step(t, m, keyMsg("j"))
	assert.Nil(t, cmd, "j must not open another feed while the line is open")
	assert.Equal(t, "h1", m.rows[m.sel].Harp, "selection pinned while typing")
	m, _ = step(t, m, keyMsg("k"))
	m, _ = step(t, m, keyMsg("space"))
	m, _ = step(t, m, keyMsg("q"))
	assert.Equal(t, "jk q", m.injectText)
	assert.Contains(t, m.render(), "inject → h1: jk q")

	// Backspace edits; enter on blank text sends nothing; esc cancels.
	m, _ = step(t, m, keyMsg("backspace"))
	assert.Equal(t, "jk ", m.injectText)
	m.injectText = "  "
	m, cmd = step(t, m, keyMsg("enter"))
	assert.Nil(t, cmd, "blank text must not send")
	assert.True(t, m.injecting)
	m, _ = step(t, m, keyMsg("esc"))
	assert.False(t, m.injecting)
	assert.Empty(t, f.injected, "cancel sends nothing")

	// And navigation works again after cancel.
	_, cmd = step(t, m, keyMsg("j"))
	require.NotNil(t, cmd, "after esc, j navigates the roster again")
}

func TestModel_InjectSendRendersDeliveryMode(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "idle"})
	f.injectMode = coord.DeliveryNewTurn
	m := openSelected(t, newTestModel(f, nil), f)

	m, _ = step(t, m, keyMsg("i"))
	m, _ = step(t, m, keyMsg("go left"))
	m, cmd := step(t, m, keyMsg("enter"))
	require.NotNil(t, cmd, "enter must dispatch the bus round trip")
	assert.False(t, m.injecting, "the line closes at send")

	m, _ = step(t, m, cmd())
	require.Equal(t, [][2]string{{"h1", "go left"}}, f.injected)
	assert.Equal(t, "injected into h1: "+coord.DeliveryNewTurn, m.status,
		"the delivery mode renders inline")
}

func TestModel_InjectTypedErrorRendersInline(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "ended"})
	f.injectErr = coord.ErrNotInjectable
	m := openSelected(t, newTestModel(f, nil), f)

	m, _ = step(t, m, keyMsg("i"))
	m, _ = step(t, m, keyMsg("hello"))
	m, cmd := step(t, m, keyMsg("enter"))
	require.NotNil(t, cmd)
	m, _ = step(t, m, cmd())
	assert.Contains(t, m.errMsg, "inject h1:")
	assert.Contains(t, m.errMsg, coord.ErrNotInjectable.Error(),
		"the bus's typed refusal renders inline")
}

func TestModel_InjectRequiresTargetAndSeam(t *testing.T) {
	// No observable sessions: i explains instead of opening.
	f := newFakeSources(t.TempDir())
	m, _ := step(t, newTestModel(f, nil), rosterMsg{rows: nil})
	m, _ = step(t, m, keyMsg("i"))
	assert.False(t, m.injecting)
	assert.Contains(t, m.status, "no agent selected")

	// No Inject seam wired (no bus can ever be reached): typed unavailability.
	f2 := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	src := f2.sources()
	src.Inject = nil
	m2 := NewModel(context.Background(), src, testGeo(), 0x1d, nil)
	m2, cmd := step(t, m2, rosterMsg{rows: f2.rows})
	require.NotNil(t, cmd)
	m2, _ = step(t, m2, cmd())
	m2, _ = step(t, m2, keyMsg("i"))
	assert.False(t, m2.injecting)
	assert.Contains(t, m2.errMsg, "inject unavailable")
}

// F1 deliverable 5 (opportunistic unit gaps): View() layouts not yet
// asserted — roster windowing around the selection (model.go:616's
// rosterLines), padCell truncation (:643), and header/follow title states.

// The overlay paints over a LIVE engine session: the controller clears and
// holds exactly totalHeight rows beneath the cursor and nothing else. A View
// that returns more lines than that — or a line wider than the terminal, which
// the terminal then wraps onto another row — writes over the engine's own
// output, and nothing puts it back. The invariant must therefore hold at every
// geometry, not just the comfortable ones.
func TestModel_ViewFitsItsGeometryExactly(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "perky-same-chevy", Agent: "developer", Engine: "claude-code", State: "live"},
		RosterRow{Harp: "swift-elm-fox", Agent: "finder", State: "executing", Depth: 1},
	)
	for _, geo := range []termui.OverlayGeometry{
		{Cols: 100, Rows: 30, PanelRows: 10},
		{Cols: 100, Rows: 30, PanelRows: 3},
		{Cols: 100, Rows: 30, PanelRows: 2},
		{Cols: 100, Rows: 30, PanelRows: 1},
		{Cols: 40, Rows: 12, PanelRows: 4},
		{Cols: 24, Rows: 8, PanelRows: 5},
		{Cols: 10, Rows: 6, PanelRows: 3},
	} {
		name := fmt.Sprintf("%dx%d", geo.Cols, geo.PanelRows)
		m := NewModel(context.Background(), f.sources(), geo, 0x1d, nil)
		m, cmd := step(t, m, rosterMsg{rows: f.rows})
		require.NotNil(t, cmd, name)
		m, _ = step(t, m, cmd())
		m = pushEntry(t, m, f, entryEv("assistant", strings.Repeat("wide output ", 12)))

		lines := strings.Split(m.render(), "\n")
		assert.Len(t, lines, geo.PanelRows, "%s: View owns exactly PanelRows rows", name)
		for i, l := range lines {
			assert.LessOrEqual(t, len([]rune(stripSGR(l))), geo.Cols,
				"%s: line %d overflows the terminal and wraps onto a row the overlay does not own", name, i)
		}
	}
}

// stripSGR removes the SGR escapes lipgloss may add, so a test can measure the
// cells a line actually occupies.
func stripSGR(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// teaKeyName's default arm is unreachable from any configuration:
// termui.ParsePrefixKey is the only production producer of the prefix byte
// (internal/cli/run_terminal_ui.go builds the overlay from it), and every byte
// that would fall through — NUL, ESC, and every printable character — it
// rejects with a reason. This pin couples the two: it goes red if
// ParsePrefixKey ever admits a byte teaKeyName does not name, and red if
// teaKeyName stops agreeing with bubbletea's own vocabulary for one.
func TestTeaKeyName_NamesEveryPrefixParsePrefixKeyAdmits(t *testing.T) {
	admitted := 0
	for ch := 0; ch < 128; ch++ {
		spelling := "ctrl-" + string(rune(ch))
		b, err := termui.ParsePrefixKey(spelling)
		if err != nil {
			continue
		}
		admitted++
		// bubbletea v2 has no exported decoder that turns a raw control byte
		// back into a Key, so the expectation is built from the key that byte
		// REPRESENTS and rendered by v2 itself. What this pins is the
		// SPELLING — that teaKeyName's hand-built "ctrl+x" is the string v2
		// renders for ctrl+x, so a change to v2's separator, modifier order or
		// naming breaks here rather than silently stopping the prefix from
		// matching. It does not re-check the byte→key arithmetic, which the
		// v1 form did via KeyType and no longer has an equivalent for.
		want := tea.KeyPressMsg(keyForControlByte(b)).String()
		assert.Equal(t, want, teaKeyName(b), "prefix %q (byte %d) must carry bubbletea's own name", spelling, b)
		assert.NotEqual(t, "ctrl+@", teaKeyName(b),
			"prefix %q (byte %d) fell through to the default arm, which names a key the user did not configure", spelling, b)
	}
	require.Greater(t, admitted, 20, "the sweep must actually exercise the accepted prefixes")
}

func TestPadCell_PadsShortAndTruncatesLongWithEllipsis(t *testing.T) {
	assert.Equal(t, "hi   ", padCell("hi", 5), "short strings are space-padded to width")
	assert.Equal(t, "hell…", padCell("hello world", 5), "long strings truncate to width-1 cells plus an ellipsis")
	assert.Equal(t, "", padCell("anything", 0), "a non-positive width truncates to empty")
}

// padCell frames COLUMNS: the pane divider has to land on the same column on
// every row or the panel visibly shears. Rune counting gets that wrong in both
// directions over the material this overlay actually renders — a CJK or emoji
// rune from an engine transcript occupies two columns, and lipgloss's own SGR
// escapes (the selected roster row is styled before it is framed) occupy none.
func TestPadCell_FramesDisplayCellsNotRunes(t *testing.T) {
	assert.Equal(t, 10, lipgloss.Width(padCell("日本語", 10)),
		"a double-width rune costs two columns, not one")

	// Written literally rather than through lipgloss: the renderer emits no
	// escapes when it detects no colour profile, which is exactly the case
	// under `go test`, so styling the string here would not exercise anything.
	styled := "\x1b[7m selected \x1b[0m"
	got := padCell(styled, 20)
	assert.Equal(t, 20, lipgloss.Width(got), "escape sequences cost no columns")
	assert.Contains(t, got, "selected", "and must not be truncated away")

	got = padCell("日本語テスト", 7)
	assert.Equal(t, 7, lipgloss.Width(got), "truncation lands on the column budget too")
	assert.True(t, strings.HasSuffix(got, "…"), "and is marked: %q", got)
}

func TestModel_RosterLinesWindowAroundSelection(t *testing.T) {
	rows := make([]RosterRow, 20)
	for i := range rows {
		rows[i] = RosterRow{Harp: fmt.Sprintf("h%d", i), State: "live"}
	}
	f := newFakeSources(t.TempDir(), rows...)
	m := openSelected(t, newTestModel(f, nil), f)

	// contentHeight() for the quick panel is testGeo().PanelRows-2 = 8, so
	// with 20 rows the pane can't show them all — select the LAST row and
	// confirm the window scrolled to keep it visible while the first row
	// falls out of view. moveDown updates m.sel (and m.feedHarp, via
	// openFeed) synchronously before returning its cmd — rosterLines only
	// reads m.sel/m.rows, so the returned feed-open cmd need not be driven
	// here (unlike the feed-content tests elsewhere in this file).
	for range rows[1:] {
		m, _ = step(t, m, keyMsg("j"))
	}
	view := m.render()
	assert.Contains(t, view, "h19", "the selected (last) row stays visible")
	assert.NotContains(t, view, "h0", "the far-scrolled-past first row leaves the visible window")
}

// TestModel_RosterLinesShowAgentNameAtRealisticDepth verifies: a live
// incident reported the agents pane's agent-name column rendering blank. A
// prior finder assessed this as an OCCLUSION artifact of a separate overlay
// geometry bug — the panel's mispositioned bottom edge let the child
// engine's own held screen bleed through where the roster pane's
// agent-name text should have been — not missing roster data — the
// same coord-fed rows already render fine in the surround's status bar. This
// test pins the DATA/RENDER path directly (independent of any real terminal
// paint): production-realistic three-word harp names, one root and one
// lineage-indented child (the incident's actual shape — a coordinator with a
// finder child), must show their "·agent" suffix in rosterLines' output.
// This passes both before and after the geometry fix (rosterLines is
// a pure string builder untouched by OverlayGeometry), which is exactly the
// point: it demonstrates the blank column was never a data/render bug, so
// the geometry fix — which stops the overlay from ever sharing the
// reserved row with stale/held screen content — is what resolves it.
func TestModel_RosterLinesShowAgentNameAtRealisticDepth(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "aloof-mean-stove", Agent: "dev", State: "executing", Depth: 0},
		RosterRow{Harp: "sixth-royal-kelp", Agent: "finder", State: "executing", Depth: 1},
	)
	m := openSelected(t, newTestModel(f, nil), f)

	lines := m.rosterLines(m.contentHeight())
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "aloof-mean-stove·dev",
		"root row's agent name renders")
	assert.Contains(t, joined, "sixth-royal-kelp·finder",
		"depth-1 (lineage-indented) child's agent name renders — the incident's actual shape")
}

// TestModel_RosterLinesTruncateNeverBlank is a companion finding, not a
// regression this task fixes: rosterPaneWidth (26) is a fixed budget
// independent of OverlayGeometry, and a long harp name plus a long
// agent-kind label (e.g. "coordinator", used elsewhere in this codebase as a
// real agent kind) on the SELECTED row (which loses one more column to the
// selection style, padCell(…, rosterPaneWidth-1)) can truncate the "·agent"
// suffix down to a couple of characters. That is a pre-existing,
// out-of-scope column-width tradeoff — NOT the incident's "blank agent-name
// column": padCell always truncates from the END, so the harp name (which
// comes first) is never the part that disappears, and the line is never
// empty/whitespace-only. This is the evidence that rosterLines cannot
// produce the incident's fully-blank column on its own — supporting the
// occlusion (overlay geometry) explanation over a data/render bug.
func TestModel_RosterLinesTruncateNeverBlank(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "aloof-mean-stove", Agent: "coordinator", State: "executing", Depth: 0},
	)
	m := openSelected(t, newTestModel(f, nil), f)

	lines := m.rosterLines(m.contentHeight())
	require.NotEmpty(t, lines)
	assert.NotEmpty(t, strings.TrimSpace(lines[0]), "the selected row's line is never blank")
	assert.Contains(t, lines[0], "aloof-mean-stove", "truncation eats the tail (agent suffix), never the harp name")
}

func TestModel_HeaderShowsAgentEngineMetaSourceAndFollowMarker(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", Agent: "dev", Engine: "claude-code", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)

	view := m.render()
	assert.Contains(t, view, "feed: h1 (dev·claude-code) · live · ▼ follow")
}

func TestModel_HeaderOmitsFollowMarkerWhenNotFollowing(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)
	m, _ = step(t, m, keyMsg("j")) // consume the first-key chord window
	m, _ = step(t, m, keyMsg("f")) // after the first key, f toggles follow off

	view := m.render()
	assert.Contains(t, view, "feed: h1")
	assert.NotContains(t, view, "▼ follow")
}

func TestModel_HeaderPlaceholderWithNoFeedSelected(t *testing.T) {
	f := newFakeSources(t.TempDir())
	m, _ := step(t, newTestModel(f, nil), rosterMsg{rows: nil})
	assert.Contains(t, m.render(), "feed: —")
}

// TestModel_ExportDirErrorWinsOverTheReturnedPath pins this. The
// production ExportDir (cli.terminalUISources) is
// `return dir, os.MkdirAll(dir, 0o755)`, so on failure it hands back a
// non-empty path for a directory that does not exist. Every consumer here must
// treat the error as decisive and never touch that path — otherwise export and
// the copy fallback would write into an unusable location, or report "saved
// <path>" for a file nobody can read. Red if any consumer is ever reordered to
// use the path first.
func TestModel_ExportDirErrorWinsOverTheReturnedPath(t *testing.T) {
	dir := t.TempDir()
	f := newFakeSources(dir, RosterRow{Harp: "h1", State: "live"})
	f.exportDirErr = fmt.Errorf("session dir unavailable")

	var copyBuf bytes.Buffer
	m := openSelected(t, newTestModel(f, &copyBuf), f)
	m = pushEntry(t, m, f, entryEv("user", "hello"))

	for _, key := range []string{"s", "S", "y"} {
		m, _ = step(t, m, keyMsg(key))
		assert.Contains(t, m.errMsg, "session dir unavailable", "key=%s: the error must reach the user", key)
		assert.NotContains(t, m.status, "saved ", "key=%s: nothing may be reported as saved", key)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries,
		"no consumer may write to the path an errored ExportDir returned alongside its error")
}

// keyForControlByte returns the bubbletea v2 Key that the raw control byte b
// stands for: bytes 1–26 are ctrl+a…ctrl+z, 28–31 are ctrl+\ ] ^ _, and 127 is
// backspace. It mirrors the terminal's own encoding, not teaKeyName's output,
// so the two can be compared.
func keyForControlByte(b byte) tea.Key {
	switch {
	case b >= 1 && b <= 26:
		return tea.Key{Code: rune('a' + b - 1), Mod: tea.ModCtrl}
	case b >= 28 && b <= 31:
		return tea.Key{Code: rune('[' + b - 27), Mod: tea.ModCtrl}
	case b == 127:
		return tea.Key{Code: tea.KeyBackspace}
	default:
		return tea.Key{Code: '@', Mod: tea.ModCtrl}
	}
}
