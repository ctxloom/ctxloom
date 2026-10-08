package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// midTierHome is a delegated run (depth 1) that can itself have children: its
// automatic turn report goes to its parent, and its agent_send may go either
// way. No coordinator is dialled, so everything it writes stays in its own
// out/ for the test to read — the calls below are synchronous, nothing waits.
func midTierHome(t *testing.T, harp string) *Home {
	t.Helper()
	resetStrictness(t)
	teeHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	home, err := NewHome(ctx, HomeConfig{
		Reporter: termSink(),
		URL:      "http://127.0.0.1:1/mcp", Token: "unused", RunID: "run-" + harp, Harness: "mock",
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Crash() })
	home.BindIdentity(coord.Identity{Harp: harp, Depth: 1})
	return home
}

func sendFrom(t *testing.T, home *Home, to, text string) {
	t.Helper()
	resp, handled := home.sendPeerViaSpool(&agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: to, Text: text, Kind: agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
		}},
	})
	require.True(t, handled)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the send must be accepted: %s", resp.GetStatus().GetMessage())
}

// autoReportsIn returns every automatic turn report sitting in harp's out/.
func autoReportsIn(t *testing.T, harp string) []coord.Message {
	t.Helper()
	var out []coord.Message
	for _, e := range spoolEntries(t, harp, spool.DirOut) {
		m, err := coord.MailFromSpool(e, e.Message.FromHarp)
		require.NoError(t, err)
		if coord.IsAutoReport(m.Structured) {
			out = append(out, m)
		}
	}
	return out
}

// TestSelfReport_SendToOwnChildStillReportsToParent pins the scope of the
// no-double-delivery rule: only a message to the PARENT is the run reporting
// in its own words. A mid-tier run that messages its own child during a turn
// has told its parent nothing, so that turn's automatic report must still go.
func TestSelfReport_SendToOwnChildStillReportsToParent(t *testing.T) {
	const harp = "mid-tier-harp"
	home := midTierHome(t, harp)

	sendFrom(t, home, "its-own-child-harp", "go check the lockfile")
	require.NoError(t, home.ReportTurnResult("I delegated the lockfile check", "", nil, nil, nil))

	got := autoReportsIn(t, harp)
	require.Len(t, got, 1, "a turn whose only send went to a child must still be reported to the parent")
	assert.Equal(t, coord.ParentAddress, got[0].To)
	assert.Contains(t, got[0].Body, "I delegated the lockfile check")
}

// TestSelfReport_SendToParentSuppressesTheReport is the other half: a turn
// that did message its parent is not reported a second time — and the
// suppression is one turn's, so the next turn reports normally.
func TestSelfReport_SendToParentSuppressesTheReport(t *testing.T) {
	const harp = "reporting-harp"
	home := midTierHome(t, harp)

	sendFrom(t, home, "its-own-child-harp", "go check the lockfile")
	sendFrom(t, home, coord.ParentAddress, "in my own words")
	require.NoError(t, home.ReportTurnResult("whatever the model happened to say", "", nil, nil, nil))
	assert.Empty(t, autoReportsIn(t, harp), "a turn that messaged its parent must not be reported twice")

	require.NoError(t, home.ReportTurnResult("the next turn", "", nil, nil, nil))
	got := autoReportsIn(t, harp)
	require.Len(t, got, 1, "the suppression must not carry into the next turn")
	assert.Contains(t, got[0].Body, "the next turn")
}
