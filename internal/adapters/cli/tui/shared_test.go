package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

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

func entryEv(role, content string) frontend.FeedEvent {
	return frontend.FeedEvent{Entry: &agent.SessionEntry{
		Type: agent.SessionEntryType(role), Content: content, Timestamp: time.Unix(1700000000, 0),
	}}
}

// teaKeyName's default arm is unreachable from any configuration:
// ParsePrefixKey is the only production producer of the prefix byte
// (internal/adapters/cli/run_terminal_ui.go starts the split view on it), and every byte
// that would fall through — NUL, ESC, and every printable character — it
// rejects with a reason. This pin couples the two: it goes red if
// ParsePrefixKey ever admits a byte teaKeyName does not name, and red if
// teaKeyName stops agreeing with bubbletea's own vocabulary for one.
func TestTeaKeyName_NamesEveryPrefixParsePrefixKeyAdmits(t *testing.T) {
	admitted := 0
	for ch := 0; ch < 128; ch++ {
		spelling := "ctrl-" + string(rune(ch))
		k, err := ParsePrefixKey(spelling)
		b := k.b
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

// The case above never reaches the truncation branch on styled input: its
// styled string is 11 columns against a 20-column budget, so it takes the
// padding branch, and its CJK cases are one code point per cluster, where a
// per-rune column scan is accidentally right. This forces the truncation
// branch on the material the split view actually frames — engine transcript text
// with SGR colour in it — where a per-rune scan cut inside the escape and put
// an unterminated CSI on the wire.
func TestPadCell_TruncatesColouredInputWithoutTearingTheEscape(t *testing.T) {
	for _, src := range []string{
		"\x1b[31mERROR\x1b[0m: build failed in pkg/foo",
		"\x1b[38;5;196mred text here\x1b[0m",
		"\x1b[38;2;220;50;47mtruecolour output line\x1b[0m",
	} {
		for w := 1; w <= 12; w++ {
			got := padCell(src, w)
			assertTerminatedEscapes(t, got, "src=%q w=%d", src, w)
			assert.Equal(t, w, lipgloss.Width(got),
				"padCell frames exactly w columns: src=%q w=%d got=%q", src, w, got)
			// The frame is padded to w either way, so width alone cannot tell
			// a good cut from a bad one. What the defect actually did was
			// spend the budget on escape bytes and pad the remainder with
			// blanks: the pane stayed the right shape and the content
			// vanished. Measure the content, not the frame.
			assert.Equal(t, w, lipgloss.Width(strings.TrimRight(got, " ")),
				"truncation spends the budget on content, not on blanks: src=%q w=%d got=%q", src, w, got)
		}
	}
}

// The pane divider is the visible consequence: every row is
// padCell(roster) + "│" + padCell(feed), so if a padCell cut tears an escape
// the terminal eats the divider and the pane beside it. Assert the divider
// lands on one column for every row, over coloured content, at real geometry.
func TestPadCell_DividerHoldsItsColumnOverColouredRows(t *testing.T) {
	rows := []string{
		"\x1b[7m selected-agent \x1b[0m",
		"\x1b[38;2;220;50;47mred-agent is running a very long tool call\x1b[0m",
		"\x1b[32mok\x1b[0m",
		"plain-agent",
	}
	const rosterW, feedW = 22, 58
	for i, r := range rows {
		cell := padCell(r, rosterW)
		line := cell + "│" + padCell(rows[len(rows)-1-i], feedW)
		assertTerminatedEscapes(t, line, "row %d", i)
		assert.Equal(t, rosterW+feedW+1, lipgloss.Width(line),
			"row %d total width shifted: %q", i, line)
		if lipgloss.Width(r) > rosterW {
			assert.Equal(t, rosterW, lipgloss.Width(strings.TrimRight(cell, " ")),
				"row %d was truncated to blanks rather than to content: %q", i, cell)
		} else {
			assert.Equal(t, lipgloss.Width(r), lipgloss.Width(strings.TrimRight(cell, " ")),
				"row %d that fits must survive intact: %q", i, cell)
		}
	}
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

// A pause says who paused the child, not a failure it waits on.
func TestHoldLabel_APauseSaysWhoPausedIt(t *testing.T) {
	assert.Equal(t, "paused by the human", HoldLabel(&coord.RunHold{Kind: coord.HoldKindHuman}))
	assert.Equal(t, "paused by its parent", HoldLabel(&coord.RunHold{Kind: coord.HoldKindAgent}))
}

// TestHoldLabel_NoDeadlineSaysOnlyWhat: a hold with no deadline of its own
// says what it waits on and nothing about when.
func TestHoldLabel_NoDeadlineSaysOnlyWhat(t *testing.T) {
	assert.Equal(t, "held: rate limited", HoldLabel(&coord.RunHold{Kind: "rate_limited"}))
	assert.Equal(t, "held: credential rejected", HoldLabel(&coord.RunHold{Kind: "credential_rejected"}))
	assert.Empty(t, HoldLabel(nil))
}

// teaKeyName at every boundary of its arms: the byte ranges are the
// terminal's control encoding, and a byte outside them names no key the
// user configured.
func TestTeaKeyName_Boundaries(t *testing.T) {
	for b, want := range map[byte]string{
		0: "ctrl+@", 1: "ctrl+a", 26: "ctrl+z", 27: "ctrl+@",
		28: "ctrl+\\", 29: "ctrl+]", 31: "ctrl+_", 32: "ctrl+@", 127: "backspace",
	} {
		assert.Equal(t, want, teaKeyName(b), "byte %d", b)
	}
}

// A feed that closes with an error delivers it; one that closes clean
// delivers none.
func TestWaitEventCmd_ClosedFeedCarriesItsError(t *testing.T) {
	boom := errors.New("boom")
	events, errs := make(chan frontend.FeedEvent), make(chan error, 1)
	close(events)
	errs <- boom
	msg := waitEventCmd("h", &Feed{Events: events, Errs: errs})()
	assert.Equal(t, feedClosedMsg{harp: "h", err: boom}, msg)

	assert.Equal(t, feedClosedMsg{harp: "h"}, waitEventCmd("h", &Feed{Events: events, Errs: make(chan error)})())
}

func TestControlOutcome_WordsEachVerb(t *testing.T) {
	req := func(verb string) coord.ControlRequest { return coord.ControlRequest{Verb: verb, Harp: "wiry-otter"} }
	for _, c := range []struct {
		msg  controlResultMsg
		want string
	}{
		{controlResultMsg{req: req(coord.ControlVerbSteer), res: coord.ControlResult{Disposition: "queued", Delivery: "mail"}}, "injected into wiry-otter: queued"},
		{controlResultMsg{req: req(coord.ControlVerbSteer), res: coord.ControlResult{Delivery: "mail"}}, "injected into wiry-otter: mail"},
		{controlResultMsg{req: req(coord.ControlVerbQuestion), res: coord.ControlResult{AskID: "a1"}}, "asked wiry-otter (ask a1); its reply arrives as mail"},
		{controlResultMsg{req: req(coord.ControlVerbSummarize), res: coord.ControlResult{AskID: "a2"}}, "asked wiry-otter (ask a2); its reply arrives as mail"},
		{controlResultMsg{req: req(coord.ControlVerbPause), res: coord.ControlResult{Changed: true}}, "paused wiry-otter"},
		{controlResultMsg{req: req(coord.ControlVerbPause)}, "wiry-otter was already paused"},
		{controlResultMsg{req: req(coord.ControlVerbResume), res: coord.ControlResult{Changed: true}}, "resumed wiry-otter"},
		{controlResultMsg{req: req(coord.ControlVerbResume)}, "wiry-otter was not paused"},
		{controlResultMsg{req: req("other")}, ""},
	} {
		note, failed := controlOutcome(c.msg)
		assert.Equal(t, c.want, note)
		assert.False(t, failed, "%s succeeded", c.want)
	}
	note, failed := controlOutcome(controlResultMsg{req: req(coord.ControlVerbSteer), err: errors.New("gone")})
	assert.Equal(t, "inject wiry-otter: gone", note)
	assert.True(t, failed)
	note, _ = controlOutcome(controlResultMsg{req: req(coord.ControlVerbQuestion), err: errors.New("gone")})
	assert.Equal(t, "ask wiry-otter: gone", note)
	note, _ = controlOutcome(controlResultMsg{req: req(coord.ControlVerbPause), err: errors.New("gone")})
	assert.Equal(t, "pause wiry-otter: gone", note)
}

func TestControlPending_WordsEachVerb(t *testing.T) {
	for verb, want := range map[string]string{
		coord.ControlVerbSteer:     "injecting into wiry-otter…",
		coord.ControlVerbQuestion:  "asking wiry-otter…",
		coord.ControlVerbSummarize: "asking wiry-otter…",
		coord.ControlVerbPause:     "pausing wiry-otter…",
		coord.ControlVerbResume:    "resuming wiry-otter…",
	} {
		assert.Equal(t, want, controlPending(coord.ControlRequest{Verb: verb, Harp: "wiry-otter"}), verb)
	}
}

func TestFeedTitleOf_NamesEachPart(t *testing.T) {
	assert.Equal(t, "feed: h", feedTitleOf(RosterRow{Harp: "h"}, "", false))
	assert.Equal(t, "feed: h (coder)", feedTitleOf(RosterRow{Harp: "h", Agent: "coder"}, "", false))
	assert.Equal(t, "feed: h (claude)", feedTitleOf(RosterRow{Harp: "h", Engine: "claude"}, "", false))
	assert.Equal(t,
		"feed: h (coder·claude) · paused by the human · ended: failed — exit 1 · ✉ 2 unread · live · ▼ follow",
		feedTitleOf(RosterRow{Harp: "h", Agent: "coder", Engine: "claude", Hold: &coord.RunHold{Kind: coord.HoldKindHuman}, Cause: "failed", Detail: "exit 1", Unread: 2}, "live", true))
	assert.Equal(t, "feed: h · ended: done", feedTitleOf(RosterRow{Harp: "h", Cause: "done"}, "", false))
	assert.Equal(t, "feed: h · ✉ 1 unread", feedTitleOf(RosterRow{Harp: "h", Unread: 1}, "", false), "a single unread message is shown")
}

// A negative width frames nothing, never a negative pad.
func TestPadCell_NegativeWidthIsEmpty(t *testing.T) {
	assert.Equal(t, "", padCell("x", -1))
}
