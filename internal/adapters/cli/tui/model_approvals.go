package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// armedMsg is termui ending a summoned modal's inert window (Overlay.Armed):
// keys reach the model from here on; the count is how many it discarded.
type armedMsg int

// noticeMsg is termui telling an engaged overlay that an approval asked for
// the screen and was not given it (Overlay.Notify).
type noticeMsg string

// approvalsUnavailable is why the approvals view cannot open.
const approvalsUnavailable = "approvals unavailable (no coordinator for this session)"

func (m Model) now() func() time.Time {
	if m.src.Now != nil {
		return m.src.Now
	}
	return time.Now
}

// openApprovals opens the approvals view over the queue. Summoned, it is the
// modal: full screen on the screen termui took, inert until armed. From the
// roster view (the a key), it takes the full screen as prefix-then-f does —
// in the panel instead when the engine is on the alternate screen.
func (m Model) openApprovals(summoned bool) (Model, tea.Cmd) {
	if m.src.Approvals == nil {
		m.reportErr(approvalsUnavailable)
		return m, nil
	}
	m.approvals, m.banner = true, ""
	m.summoned, m.arming = summoned, summoned
	m.appr = newApprovalsModel(m.src.Approvals, m.now(), m.prefixKey)
	m.appr.events = m.src.Approvals.Subscribe(m.ctx)
	m.appr.resync()
	cmds := []tea.Cmd{m.appr.initCmds()}
	if summoned || !m.geo.EngineOnAltScreen {
		m.full = true
		m.resize()
		cmds = append(cmds, m.ownTerminalCmd())
	}
	return m, tea.Batch(cmds...)
}

// updateApprovalsKey hands the key to the approvals view; a hide closes the
// overlay, like Later.
func (m Model) updateApprovalsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var hide bool
	m.appr, cmd, hide = m.appr.key(msg)
	if hide {
		return m.quit()
	}
	return m, cmd
}

// applyChromeMsg lands what termui tells the overlay about the modal: the
// end of its arming, or a notice to show while the roster view is open.
func (m Model) applyChromeMsg(msg tea.Msg) Model {
	switch msg := msg.(type) {
	case armedMsg:
		m.arming, m.armDiscarded = false, int(msg)
	case noticeMsg:
		if !m.approvals {
			m.banner = "⚑ " + sanitizeForDisplay(string(msg)) + " — a to review"
		}
	}
	return m
}

// applyApprovalsMsg lands a paste, the queue's and the decisions' messages.
// The view closes itself once the list is empty — the modal on its own, the
// a-opened view back to the roster.
func (m Model) applyApprovalsMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.approvals {
		return m, nil
	}
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.appr = m.appr.paste(msg.Content)
		return m, nil
	case queueEventMsg:
		m.appr, cmd = m.appr.applyQueueEvent(msg)
	case approvalsTickMsg:
		m.appr, cmd = m.appr.applyTick()
	case answerResultMsg:
		m.appr = m.appr.applyAnswerResult(msg)
	case revokeResultMsg:
		m.appr = m.appr.applyRevokeResult(msg)
	}
	if !m.appr.done() {
		return m, cmd
	}
	if m.summoned {
		return m.quit()
	}
	m.approvals = false
	m.reportOK("no approvals waiting")
	return m, nil
}
