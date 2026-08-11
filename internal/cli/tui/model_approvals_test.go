package tui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// TestModel_ApprovalsKeyOnEmptyListHintsWithoutOpening pins the "a" key's
// refusal to open on nothing to show — the same shape openInject's "no agent
// selected" hint uses, not a blank panel.
func TestModel_ApprovalsKeyOnEmptyListHintsWithoutOpening(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	m := openSelected(t, newTestModel(f, nil), f)

	m, cmd := step(t, m, keyMsg("a"))
	assert.Nil(t, cmd, "an empty list needs no round trip")
	assert.False(t, m.approving)
	assert.Contains(t, m.status, "no pending approvals")
}

// TestModel_ApprovalsKeyWithNoSeamHintsUnavailable mirrors
// TestModel_InjectRequiresTargetAndSeam's nil-seam half: a Sources with no
// PendingApprovals wired (no coordinator hosted) must not open the view, and
// must say why.
func TestModel_ApprovalsKeyWithNoSeamHintsUnavailable(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	src := f.sources()
	src.PendingApprovals = nil
	m := NewModel(context.Background(), src, testGeo(), 0x1d, nil)
	m, cmd := step(t, m, rosterMsg{rows: f.rows})
	require.NotNil(t, cmd)
	m, _ = step(t, m, cmd())

	m, cmd = step(t, m, keyMsg("a"))
	assert.Nil(t, cmd)
	assert.False(t, m.approving)
	assert.Contains(t, m.errMsg, "approvals unavailable")
}

// TestModel_ApprovalsOpenSelectAndAnswer covers y/s driving AnswerApproval
// with the SELECTED (not the first) approval's messageID+harp, the correct
// decision, and an empty note — pinned per key so a swapped accept/decline
// mapping goes red on the specific subtest naming the swap.
func TestModel_ApprovalsOpenSelectAndAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      string
		decision agentcoordpb.ApprovalDecision_Decision
	}{
		{"y-accepts", "y", agentcoordpb.ApprovalDecision_DECISION_ACCEPT},
		{"s-accepts-for-session", "s", agentcoordpb.ApprovalDecision_DECISION_ACCEPT_FOR_SESSION},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
			f.approvals = []coord.PendingApproval{
				{MessageID: "m-1", Harp: "child-1", Title: "bash"},
				{MessageID: "m-2", Harp: "child-2", Title: "write"},
			}
			m := openSelected(t, newTestModel(f, nil), f)

			m, cmd := step(t, m, keyMsg("a"))
			require.True(t, m.approving)
			assert.Nil(t, cmd, "opening reads PendingApprovals synchronously — no round trip to wait on")
			assert.Contains(t, m.render(), "approvals (2 pending)")

			// Move off the first entry so the test actually exercises "the
			// SELECTED approval", not just index 0.
			m, _ = step(t, m, keyMsg("j"))
			require.Equal(t, 1, m.approvalSel)

			m, cmd = step(t, m, keyMsg(tc.key))
			require.NotNil(t, cmd, "the answer round trip runs as a cmd")
			m, _ = step(t, m, cmd())

			require.Len(t, f.answered, 1)
			got := f.answered[0]
			assert.Equal(t, "m-2", got.messageID, "the SECOND (selected) entry, not the first")
			assert.Equal(t, "child-2", got.harp)
			assert.Equal(t, tc.decision, got.decision)
			assert.Empty(t, got.note, "y/s carry no note")
		})
	}
}

// TestModel_ApprovalsDeclineNoteAccumulatesSendsAndEscCancelsWithoutAnswering
// covers n's note line: esc backs out to the LIST (not the whole overlay)
// WITHOUT calling AnswerApproval, and enter sends DECISION_DECLINE with the
// composed (and backspace-edited) note.
func TestModel_ApprovalsDeclineNoteAccumulatesSendsAndEscCancelsWithoutAnswering(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	f.approvals = []coord.PendingApproval{{MessageID: "m-1", Harp: "child-1", Title: "bash"}}
	m := openSelected(t, newTestModel(f, nil), f)

	m, _ = step(t, m, keyMsg("a"))
	require.True(t, m.approving)

	// esc path: open the note, type into it, back out. Nothing sent, and the
	// approvals LIST (not the roster/feed) is what's left open.
	m, _ = step(t, m, keyMsg("n"))
	require.True(t, m.approvalNoting)
	m, _ = step(t, m, keyMsg("h"))
	m, _ = step(t, m, keyMsg("i"))
	assert.Equal(t, "hi", m.approvalNoteText)
	assert.Contains(t, m.render(), "decline note: hi")

	m, _ = step(t, m, keyMsg("esc"))
	assert.False(t, m.approvalNoting)
	assert.True(t, m.approving, "esc backs out to the list, not the whole overlay")
	assert.Empty(t, f.answered, "esc must not call AnswerApproval")

	// send path: reopen, type, backspace-edit, enter.
	m, _ = step(t, m, keyMsg("n"))
	m, _ = step(t, m, keyMsg("h"))
	m, _ = step(t, m, keyMsg("i"))
	m, _ = step(t, m, keyMsg("backspace"))
	m, cmd := step(t, m, keyMsg("enter"))
	require.NotNil(t, cmd, "enter dispatches the answer round trip")
	assert.False(t, m.approvalNoting, "the note line closes at send")
	m, _ = step(t, m, cmd())

	require.Len(t, f.answered, 1)
	got := f.answered[0]
	assert.Equal(t, "m-1", got.messageID)
	assert.Equal(t, "child-1", got.harp)
	assert.Equal(t, agentcoordpb.ApprovalDecision_DECISION_DECLINE, got.decision)
	assert.Equal(t, "h", got.note, "backspace edited 'hi' down to 'h' before send")
}

// TestModel_ApprovalsRefreshOnTickClampsSelectionAndDropsResolved pins the
// rosterTickMsg-driven refresh: an approval resolved elsewhere (another
// answerer, or a timeout) disappears from the list, the selection clamps
// into range rather than going out of bounds, and an emptied list closes the
// view with the same hint the "a" key's empty-list guard uses — never a
// blank panel left open.
func TestModel_ApprovalsRefreshOnTickClampsSelectionAndDropsResolved(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	f.approvals = []coord.PendingApproval{
		{MessageID: "m-1", Harp: "child-1", Title: "bash"},
		{MessageID: "m-2", Harp: "child-2", Title: "write"},
		{MessageID: "m-3", Harp: "child-3", Title: "read"},
	}
	m := openSelected(t, newTestModel(f, nil), f)
	m, _ = step(t, m, keyMsg("a"))
	m, _ = step(t, m, keyMsg("j"))
	m, _ = step(t, m, keyMsg("j"))
	require.Equal(t, 2, m.approvalSel, "selected the last entry")

	// Another answerer resolves m-2 and m-3 (the selected one) between ticks.
	f.approvals = []coord.PendingApproval{{MessageID: "m-1", Harp: "child-1", Title: "bash"}}
	m, _ = step(t, m, rosterTickMsg{})
	assert.Len(t, m.approvals, 1, "resolved-elsewhere entries disappear on refresh")
	assert.Equal(t, 0, m.approvalSel, "selection clamps back into range")
	assert.True(t, m.approving, "one approval still pending: the view stays open")

	// The last one resolves too.
	f.approvals = nil
	m, _ = step(t, m, rosterTickMsg{})
	assert.False(t, m.approving, "an emptied list closes the view")
	assert.Contains(t, m.status, "no pending approvals")
}

// TestModel_ApprovalAnswerWithNoAnswerSeamReportsUnavailable covers a
// Sources with PendingApprovals wired but AnswerApproval nil (should not
// happen in production wiring, but the model must not panic on it): y must
// report the same "unavailable" text rather than dereferencing a nil func.
func TestModel_ApprovalAnswerWithNoAnswerSeamReportsUnavailable(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	f.approvals = []coord.PendingApproval{{MessageID: "m-1", Harp: "child-1", Title: "bash"}}
	src := f.sources()
	src.AnswerApproval = nil
	m := NewModel(context.Background(), src, testGeo(), 0x1d, nil)
	m, cmd := step(t, m, rosterMsg{rows: f.rows})
	require.NotNil(t, cmd)
	m, _ = step(t, m, cmd())

	m, _ = step(t, m, keyMsg("a"))
	require.True(t, m.approving)
	m, cmd = step(t, m, keyMsg("y"))
	require.NotNil(t, cmd)
	m, _ = step(t, m, cmd())
	assert.Contains(t, m.errMsg, "approvals unavailable")
}
