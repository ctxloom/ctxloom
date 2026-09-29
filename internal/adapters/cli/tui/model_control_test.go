package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// The viewer drives all five control verbs through ONE seam
// (Sources.Control): steer, question and summarize carry a body typed into
// the input line; pause and resume act on the viewed harp at the keypress.

// sendVia opens the input line with key, types body, and sends it.
func sendVia(t *testing.T, m Model, key, body string) (Model, func() any) {
	t.Helper()
	m, _ = step(t, m, keyMsg(key))
	m, _ = step(t, m, keyMsg(body))
	m, cmd := step(t, m, keyMsg("enter"))
	require.NotNil(t, cmd, "enter must dispatch the %q request", key)
	return m, func() any { return cmd() }
}

func TestModel_ControlKeysDriveEveryVerb(t *testing.T) {
	for _, tc := range []struct {
		key, body, verb string
	}{
		{"i", "go left", coord.ControlVerbSteer},
		{"?", "why this approach", coord.ControlVerbQuestion},
		{"s", "the open risks", coord.ControlVerbSummarize},
		{"p", "", coord.ControlVerbPause},
		{"r", "", coord.ControlVerbResume},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "executing"})
			m := openSelected(t, newTestModel(f), f)

			var run func() any
			if tc.body != "" {
				m, run = sendVia(t, m, tc.key, tc.body)
			} else {
				var cmd func() any
				m2, c := step(t, m, keyMsg(tc.key))
				require.NotNil(t, c, "%q acts at the keypress, with no input line", tc.key)
				assert.Empty(t, m2.composeVerb)
				cmd = func() any { return c() }
				m, run = m2, cmd
			}
			m, _ = step(t, m, run())
			require.Equal(t, []coord.ControlRequest{{Verb: tc.verb, Harp: "h1", Body: tc.body}}, f.controlled)
			assert.Empty(t, m.errMsg)
		})
	}
}

func TestModel_AskAndSummarizeRenderTheAnswer(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"?", "answer from h1: first line second line"},
		{"s", "summary from h1: first line second line"},
	} {
		f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "executing"})
		f.controlOut = coord.ControlResult{Answer: &coord.AskAnswer{From: "h1", Text: "first line\n  second line\n"}}
		m := openSelected(t, newTestModel(f), f)

		m, run := sendVia(t, m, tc.key, "anything")
		assert.Contains(t, m.status, "h1", "the pending ask names its target while it waits")
		m, _ = step(t, m, run())
		assert.Equal(t, tc.want, m.status, "the answer lands on the one-line hint bar, flattened")
	}
}

func TestModel_PauseAndResumeSayWhetherAnythingChanged(t *testing.T) {
	for _, tc := range []struct {
		key     string
		changed bool
		want    string
	}{
		{"p", true, "paused h1"},
		{"p", false, "h1 was already paused"},
		{"r", true, "resumed h1"},
		{"r", false, "h1 was not paused"},
	} {
		f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "executing"})
		f.controlOut = coord.ControlResult{Changed: tc.changed}
		m := openSelected(t, newTestModel(f), f)

		m, cmd := step(t, m, keyMsg(tc.key))
		require.NotNil(t, cmd)
		m, _ = step(t, m, cmd())
		assert.Equal(t, tc.want, m.status)
	}
}

func TestModel_ControlErrorNamesTheVerb(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "executing"})
	f.controlErr = coord.ErrAskTimeout
	m := openSelected(t, newTestModel(f), f)

	m, run := sendVia(t, m, "?", "still there?")
	m, _ = step(t, m, run())
	assert.Contains(t, m.errMsg, "ask h1:")
	assert.Contains(t, m.errMsg, coord.ErrAskTimeout.Error())
}

func TestModel_ControlKeysNeedATargetAndTheSeam(t *testing.T) {
	for _, key := range []string{"?", "s", "p", "r"} {
		f := newFakeSources(t.TempDir())
		m, _ := step(t, newTestModel(f), rosterMsg{rows: nil})
		m, cmd := step(t, m, keyMsg(key))
		assert.Nil(t, cmd, key)
		assert.Empty(t, m.composeVerb, key)
		assert.Contains(t, m.status, "no agent selected", key)

		f2 := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
		m2 := openSelected(t, newTestModel(f2), f2)
		m2.src.Control = nil
		m2, cmd = step(t, m2, keyMsg(key))
		assert.Nil(t, cmd, key)
		assert.Empty(t, m2.composeVerb, key)
		assert.Contains(t, m2.errMsg, "unavailable", key)
	}
}

func TestModel_FooterNamesEveryControlKey(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f), f)
	footer := m.footerLine(400)
	for _, hint := range []string{"i inject", "? ask", "s summarize", "p pause", "r resume", "a approvals"} {
		assert.Contains(t, footer, hint)
	}
	assert.NotContains(t, footer, "save", "the transcript export was removed; the footer must not advertise it")
	assert.NotContains(t, footer, "copy", "the OSC-52 copy was removed; the footer must not advertise it")

	m.src.PendingApprovals = nil
	assert.NotContains(t, m.footerLine(400), "a approvals", "a pane that is not wired must not be advertised")

	m, _ = step(t, m, keyMsg("?"))
	assert.Contains(t, m.render(), "ask → h1:", "the input line names the verb and its target")
	m, _ = step(t, m, keyMsg("esc"))
	m, _ = step(t, m, keyMsg("s"))
	assert.Contains(t, m.render(), "summarize → h1:")
}

// TestModel_FooterNoteSurvivesAnOrdinaryWidth: the note is the outcome of the
// key just pressed. Appended after the full key list it was cut off at any
// ordinary width, so every failure and confirmation was invisible at 80
// columns; it leads the line, and the key list is what gets truncated.
func TestModel_FooterNoteSurvivesAnOrdinaryWidth(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f), f)
	m.reportErr("inject: engine refused")
	assert.Contains(t, m.footerLine(80), "inject: engine refused")
}
