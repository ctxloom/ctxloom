package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeNotifier records the desktop notifications the split view raised.
type fakeNotifier struct{ raised [][2]string }

func (f *fakeNotifier) Notify(title, body string) {
	f.raised = append(f.raised, [2]string{title, body})
}

func withNotifier(n Notifier) harnessOpt { return func(o *SplitOptions) { o.Notify = n } }

// TestSplit_AParkedApprovalAlertsTheDesktopWithTheBell: an arrival raises a
// desktop notification beside the bell, under the bell's own rate limit — a
// burst inside approvalBellInterval alerts once, the next one after it alerts
// again — and neither fires while the approvals view is open, nor for a
// count that is no arrival.
func TestSplit_AParkedApprovalAlertsTheDesktopWithTheBell(t *testing.T) {
	n := &fakeNotifier{}
	h := newSplitHarness(t, newSplitPort(), withNotifier(n)).sized(100, 12)

	h.send(approvalsCountMsg{n: 1, arrived: true})
	assert.Equal(t, [][2]string{{alertTitle, "root-otter: 1 approval waiting"}}, n.raised)
	assert.Equal(t, []string{"\a"}, h.raws())

	h.advance(approvalBellInterval - time.Nanosecond)
	h.send(approvalsCountMsg{n: 2, arrived: true})
	assert.Len(t, n.raised, 1, "a burst alerts once")
	assert.Len(t, h.raws(), 1, "and rings once")

	h.send(approvalsCountMsg{n: 2})
	h.advance(time.Nanosecond)
	h.send(approvalsCountMsg{n: 2})
	assert.Len(t, n.raised, 1, "a count that is no arrival alerts nobody")

	h.send(approvalsCountMsg{n: 3, arrived: true})
	assert.Equal(t, [2]string{alertTitle, "root-otter: 3 approvals waiting"}, n.raised[1], "the interval spent, the next arrival alerts")
	assert.Len(t, h.raws(), 2)

	h.s.apprOpen = true
	h.advance(approvalBellInterval)
	h.send(approvalsCountMsg{n: 4, arrived: true})
	assert.Len(t, n.raised, 2, "the open approvals view is its own signal")
}

// TestSplit_NoNotifierStillRings: a split view with no desktop notifier
// rings the bell alone.
func TestSplit_NoNotifierStillRings(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	h.send(approvalsCountMsg{n: 1, arrived: true})
	assert.Equal(t, []string{"\a"}, h.raws())
}
