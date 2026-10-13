package tui

import (
	"bytes"
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"
)

// FenceKey and FenceMarker are a TEST-ONLY ordering fence: nothing in the
// product types FenceKey or waits for FenceMarker. A test that drives an
// overlay through its input writes FenceKey(n); the overlay answers by
// writing FenceMarker(n) to its terminal once
//
//   - every message before the fence key has been applied — keys included,
//     since the fence rides the same input stream and parser as they do;
//   - every background command those messages started (a roster fetch, a feed
//     open, a control round trip, an approval decision) has returned and its
//     result has been applied; and
//   - the frame showing all of that has been written.
//
// So the screen up to the marker is the overlay's settled answer to
// everything before the fence, judged once, instead of a wait for a frame
// that a wrong overlay may never paint.
//
// The key is a kitty-protocol key press (CSI <code> u) whose code is in
// Unicode's Supplementary Private Use Area-B, which no keyboard sends, and
// termui routes it to the overlay as a key. The marker is a DECRQM query:
// inert to the screen. A fence key is not a key the human pressed: it is
// never seen by the key bindings and does not spend prefix-then-f.
// n ranges over [0, 65533].
func FenceKey(n int) string { return "\x1b[" + strconv.Itoa(int(fenceKeyBase)+n) + "u" }

// FenceMarker is what the overlay writes for FenceKey(n). See FenceKey.
func FenceMarker(n int) string { return fmt.Sprintf("\x1b[?%d$p", fenceMarkerBase+n) }

const (
	fenceKeyBase    rune = 0x100000 // Supplementary Private Use Area-B
	fenceKeyLast    rune = 0x10FFFD
	fenceMarkerBase      = 79000
)

// fenceTokenPrefix opens the private token the model asks bubbletea to write
// for a released fence; onlcr strips it before the terminal sees it. An APC
// string, so a token that ever escaped would still be inert.
const fenceTokenPrefix = "\x1b_ctxloom-fence;"

func fenceToken(n int) string { return fenceTokenPrefix + strconv.Itoa(n) + "\x1b\\" }

// fenceWriteMsg is onlcr reporting that fence n's token was flushed.
type fenceWriteMsg int

// background counts the overlay's one-shot background commands in flight and
// holds the fences waiting for them. One per model, shared by its copies;
// only the program's event loop touches it (Init, Update), so it needs no
// lock.
type background struct {
	inFlight int
	held     []int
}

// bgDoneMsg is a tracked command's result, marking it no longer in flight.
type bgDoneMsg struct{ msg tea.Msg }

// track counts cmd as in flight until its result is applied. Only one-shot
// commands may be tracked: one that waits on a stream (a feed's events, the
// approval queue) or a timer would hold every fence for as long as it waits.
func (m Model) track(cmd tea.Cmd) tea.Cmd {
	if cmd == nil || m.bg == nil {
		return cmd
	}
	m.bg.inFlight++
	return func() tea.Msg { return bgDoneMsg{cmd()} }
}

// fenceKey reports whether msg is a FenceKey, and which.
func fenceKey(msg tea.KeyPressMsg) (int, bool) {
	if msg.Code < fenceKeyBase || msg.Code > fenceKeyLast || msg.Mod != 0 {
		return 0, false
	}
	return int(msg.Code - fenceKeyBase), true
}

// applyFence holds fence n until nothing is in flight.
func (m Model) applyFence(n int) (tea.Model, tea.Cmd) {
	if m.bg == nil {
		m.bg = &background{}
	}
	m.bg.held = append(m.bg.held, n)
	return m, m.releaseFences()
}

// keyOrFence routes a key press: a FenceKey to the fence, anything else to
// the key bindings, which never see a fence.
func (m Model) keyOrFence(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if n, ok := fenceKey(msg); ok {
		return m.applyFence(n)
	}
	return m.updateKey(msg)
}

// applyBackgroundMsg lands what background work hands back: a control round
// trip's outcome, a tracked command's result, or a flushed fence token.
func (m Model) applyBackgroundMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case controlResultMsg:
		return m.applyControlResult(msg)
	case fenceWriteMsg:
		return m, tea.Raw(FenceMarker(int(msg)))
	}
	return m.applyBgDone(msg.(bgDoneMsg))
}

// applyBgDone applies a tracked command's result, then releases the fences
// it was the last thing holding.
func (m Model) applyBgDone(msg bgDoneMsg) (tea.Model, tea.Cmd) {
	m.bg.inFlight--
	next, cmd := m.Update(msg.msg)
	return next, tea.Batch(cmd, next.(Model).releaseFences())
}

// releaseFences asks bubbletea to write the held fences' tokens, in order,
// once nothing is in flight. The token goes through the program's raw
// output, which it flushes BEFORE each frame: so onlcr seeing it means the
// frame of everything so far is next on that same flush, and the marker the
// model writes for fenceWriteMsg can only reach the terminal after it.
func (m Model) releaseFences() tea.Cmd {
	if m.bg == nil || m.bg.inFlight > 0 || len(m.bg.held) == 0 {
		return nil
	}
	var b bytes.Buffer
	for _, n := range m.bg.held {
		b.WriteString(fenceToken(n))
	}
	m.bg.held = nil
	return tea.Raw(b.String())
}

// splitFenceTokens removes every fence token from p, returning the rest and
// the fences named, in order.
func splitFenceTokens(p []byte) ([]byte, []int) {
	var fences []int
	for {
		i := bytes.Index(p, []byte(fenceTokenPrefix))
		if i < 0 {
			return p, fences
		}
		rest := p[i+len(fenceTokenPrefix):]
		j := bytes.Index(rest, []byte("\x1b\\"))
		if j < 0 {
			return p, fences
		}
		if n, err := strconv.Atoi(string(rest[:j])); err == nil {
			fences = append(fences, n)
		}
		p = append(append([]byte{}, p[:i]...), rest[j+2:]...)
	}
}
