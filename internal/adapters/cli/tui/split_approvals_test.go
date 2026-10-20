package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// hostileCommand tries everything a child could put in a Bash command to
// repaint the approvals view: clear and home, a fake button, an OSC title,
// a bidi override.
const hostileCommand = "rm -rf build\x1b[2J\x1b[H[ Allow ]\x1b]0;pwned\x07 \u202egnp.exe"

// TestSplit_ASummonedApprovalShowsTheChildsEscapesAsText: a summoned
// approval takes the pane framed under its trust line, inert while it arms,
// and the child's escapes are on screen as text — so none of them acted.
// Navigate and Enter, once armed, answer the request it was aimed at.
func TestSplit_ASummonedApprovalShowsTheChildsEscapesAsText(t *testing.T) {
	port := newSplitPort()
	q := newFakeQueue(bashReq("a", "wiry-otter", hostileCommand, 10*time.Minute))
	port.queue = q
	h := newSplitHarness(t, port).sized(140, 30)
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, notice: "approval from wiry-otter", reply: reply})
	require.NoError(t, <-reply)

	screen := strings.Join(h.screen(), "\n")
	assert.Contains(t, screen, "┌ ctxloom approval ─ keys here never reach the engine")
	assert.Contains(t, screen, "$ rm -rf build⟨ESC⟩[2J⟨ESC⟩[H[ Allow ]⟨ESC⟩]0;pwned⟨U+0007⟩ ⟨U+202E⟩gnp.exe")
	assert.Contains(t, screen, "arming — keys are ignored for a moment")

	h.advance(h.s.present.ArmFor)
	h.keys("right")
	assert.Contains(t, strings.Join(h.screen(), "\n"), "[ Allow once ]")
	h.keys("enter")
	require.Len(t, q.calls(), 1)
	assert.Equal(t, answerCall{id: "a", d: coord.ApprovalDecision{Allow: true}}, q.calls()[0])
}
