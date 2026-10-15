package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// rosterUnread is harp's unread count as each roster projection reports it:
// the in-process roster (the root's surfaces) and the wire roster (an agent's
// roster tool). The two are one fact and must not disagree.
func rosterUnread(c *Coordinator, harp string) (inProcess, wire int) {
	inProcess, wire = -1, -1
	for _, e := range c.Roster(ownerIdentity()) {
		if e.Harp == harp {
			inProcess = e.Unread
		}
	}
	for _, r := range c.listRunsSnapshot(true, "", "").Runs {
		if r.Agent != nil && r.Agent.AgentID == harp {
			wire = r.Unread
		}
	}
	return inProcess, wire
}

// TestRoster_CountsAChildsUnreadMailUntilItsTurnTakesIt is alpha issue 1's
// visibility half: a parent's reply sat unread through a child's whole long
// turn and nothing said so. Mail sent to a child that is mid-turn waits in its
// inbox, and the roster says how much waits; once the child's next turn has
// taken it, it is no longer unread.
func TestRoster_CountsAChildsUnreadMailUntilItsTurnTakesIt(t *testing.T) {
	c, sp, out := heldChild(t)
	in, wire := rosterUnread(c, out.Harp)
	require.Equal(t, 0, in, "nothing has been sent yet")
	require.Equal(t, 0, wire)

	_, err := c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "binding reply", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		in, wire := rosterUnread(c, out.Harp)
		return in == 1 && wire == 1
	}, conformanceWait, 10*time.Millisecond, "a mid-turn child's waiting mail must show on BOTH rosters")

	sc := sp.chat(0)
	sc.Mu.Lock()
	gate := sc.Gate
	sc.Gate = nil
	sc.Mu.Unlock()
	close(gate)
	awaitChatText(t, sp, 0, "binding reply")
	require.Eventually(t, func() bool {
		in, wire := rosterUnread(c, out.Harp)
		return in == 0 && wire == 0
	}, conformanceWait, 10*time.Millisecond, "mail the child's turn took is no longer unread")
}

// TestUnreadMail_WarnsOnlyWhenTheInboxCannotBeRead: an inbox the roster
// cannot read counts as empty and says so; one it reads (here, one never
// created) is counted without a word.
func TestUnreadMail_WarnsOnlyWhenTheInboxCannotBeRead(t *testing.T) {
	resetStrictness(t)
	var findings report.Collector
	c := newTestCoordinatorWith(t, newFakeSpawner(t, nil, nil), func(o *Options) { o.Reporter = &findings })

	assert.Zero(t, c.unreadMail("quiet-harp"))
	assert.Empty(t, findings.All().Texts(), "a readable inbox is not warned about")

	in, err := spool.DirPath(c.mapper, "broken-harp", spool.DirIn)
	require.NoError(t, err)
	testsupport.WriteFile(t, c.root.Fs, in, []byte("not a directory"), 0o600)
	assert.Zero(t, c.unreadMail("broken-harp"))
	texts := findings.All().Texts()
	require.Len(t, texts, 1)
	assert.Contains(t, texts[0], "coordinator: counting broken-harp's unread mail")
}
