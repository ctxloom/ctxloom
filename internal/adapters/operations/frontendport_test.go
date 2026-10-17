package operations

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/frontendcontract"
	"github.com/ctxloom/ctxloom/internal/testsupport/livecoord"
)

// TestFrontendPort_Contract runs the frontend port's contract against the
// in-process port over a live coordinating session (livecoord) and a real pty
// whose engine is the contract's echo terminal.
func TestFrontendPort_Contract(t *testing.T) {
	frontendcontract.Run(t, newInprocWorld, func(t *testing.T) frontend.Port {
		home := testsupport.Isolate(t)
		return NewFrontendPort(FrontendDeps{Engines: engines.Registry(), WorkDir: home, Self: "no-coordinator"})
	})
}

// echoTerminal is the contract's interactive engine: "ready", then for each
// line typed "got:<line>" and the window size.
const echoTerminal = `printf '` + frontendcontract.TerminalReady + `\n'; while IFS= read -r l; do printf 'got:%s\n' "$l"; stty size; done`

// inprocWorld is a live session whose port is NewFrontendPort's.
type inprocWorld struct {
	*livecoord.Session
	port frontend.Port
}

func newInprocWorld(t *testing.T) frontendcontract.World {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	s := livecoord.New(t, projectDir, frontendcontract.ContextWindow)
	ctx, cancel := context.WithCancel(context.Background())
	pty, err := hostpty.Start(ctx, exec.Command("sh", "-c", echoTerminal))
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); pty.Kill() })
	return &inprocWorld{Session: s, port: NewFrontendPort(FrontendDeps{
		Coord: s.Coord, Engines: engines.Registry(), WorkDir: projectDir, Self: livecoord.OwnerHarp, Terminal: pty,
	})}
}

func (w *inprocWorld) Port() frontend.Port { return w.port }
func (w *inprocWorld) Self() string        { return livecoord.OwnerHarp }

func (w *inprocWorld) Ask(t *testing.T, harp, tool string) <-chan coord.ApprovalDecision {
	return w.Session.Ask(t, harp, tool)
}

func (w *inprocWorld) FailNextTurn(harp string, kind agent.FailureKind) {
	w.Session.FailNextTurn(harp, kind)
}

func (w *inprocWorld) Stop(t *testing.T, harp string) { w.Session.Stop(t, harp) }

// TestConsumerService_CarriesWhatThePortGrew: the observation the frontend
// port grew reaches ConsumerService itself, so a frontend in another process
// reads the same picture over the wire — a child's parent harp, last
// activity and context gauge on ListRuns, and the holds in force on
// CredentialHolds (names only).
func TestConsumerService_CarriesWhatThePortGrew(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	s := livecoord.New(t, projectDir, frontendcontract.ContextWindow)
	child := s.Delegate(t, "over the wire")

	cs, _ := DiscoverCoordinators()
	require.Len(t, cs, 1)
	client, conn, err := dialConsumer(cs[0].Endpoint)
	require.NoError(t, err)
	defer conn.Close()
	ctx := context.Background()

	var row *agentcoordpb.ListRunsResult_RunInfo
	require.Eventually(t, func() bool {
		runs, err := client.ListRuns(ctx, &agentcoordpb.ListRunsRequest{IncludeTerminal: true})
		require.NoError(t, err)
		for _, r := range runs.GetRuns() {
			if r.GetAgent().GetAgentId() == child && r.GetContextTokensUsed() > 0 {
				row = r
				return true
			}
		}
		return false
	}, 30*time.Second, 10*time.Millisecond)
	assert.Equal(t, livecoord.OwnerHarp, row.GetParentHarp())
	assert.EqualValues(t, frontendcontract.ContextTokens, row.GetContextTokensUsed())
	assert.EqualValues(t, frontendcontract.ContextWindow, row.GetContextWindow())
	assert.NotNil(t, row.GetLastEventAt(), "the row says when the run last moved")

	s.FailNextTurn(child, agent.FailureOverloaded)
	_, err = s.Coord.Control(ctx, coord.ControlInitiator{Kind: coord.InitiatorHuman}, coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: child, Body: "overload"})
	require.NoError(t, err)
	var hold *agentcoordpb.CredentialHoldsResult_Hold
	require.Eventually(t, func() bool {
		res, err := client.CredentialHolds(ctx, &agentcoordpb.CredentialHoldsRequest{})
		require.NoError(t, err)
		for _, h := range res.GetHolds() {
			if slices.Contains(h.GetHarps(), child) {
				hold = h
				return true
			}
		}
		return false
	}, 30*time.Second, 10*time.Millisecond)
	assert.Equal(t, string(agent.FailureOverloaded), hold.GetKind())
	assert.Equal(t, livecoord.Engine, hold.GetEngine())
	assert.NotNil(t, hold.GetSince())
	assert.NotNil(t, hold.GetUntil(), "an overload backs off until a deadline")
}

// The port's feed carries entries and gaps; a response boundary or a
// heartbeat marks the stream and is not an item.
func TestFrontendFeedEvent(t *testing.T) {
	entry := &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hi"}
	for _, tc := range []struct {
		name  string
		in    SessionFeedEvent
		want  frontend.FeedEvent
		shown bool
	}{
		{"an entry", SessionFeedEvent{Event: &transcript.WatchEvent{Entry: entry}}, frontend.FeedEvent{Entry: entry}, true},
		{"a gap", SessionFeedEvent{Gap: 7}, frontend.FeedEvent{Gap: 7}, true},
		{"a boundary", SessionFeedEvent{Event: &transcript.WatchEvent{Boundary: &transcript.ResponseBoundary{ToIndex: 2}}}, frontend.FeedEvent{}, false},
		{"a heartbeat", SessionFeedEvent{Event: &transcript.WatchEvent{Heartbeat: true}}, frontend.FeedEvent{}, false},
		{"nothing", SessionFeedEvent{}, frontend.FeedEvent{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, shown := frontendFeedEvent(tc.in)
			assert.Equal(t, tc.shown, shown)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestFrontendPort_ListSessions_StateIsTheLocksVerdict: a session's state is
// a CLAIM about now and its ended_at a FACT about the past; the two disagree
// in both directions, so the port's state comes from the session lock alone.
// A held lock is a running session even with ended_at set (a resume), a free
// lock is an ended one even without it (a crash), and no lock file is the
// third honest answer, whatever ended_at says.
func TestFrontendPort_ListSessions_StateIsTheLocksVerdict(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := home + "/proj"
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	assign := func(ended bool) string {
		e, err := mgr.AssignHarp(projectDir, "claude-code")
		require.NoError(t, err)
		if ended {
			require.NoError(t, mgr.MarkEnded(e.HarpName, time.Now().Add(-time.Hour)))
		}
		return e.HarpName
	}
	resumed, crashed, unlockedEnded, unlocked := assign(true), assign(false), assign(true), assign(false)
	require.NoError(t, sessionlock.Hold(resumed))
	t.Cleanup(func() { sessionlock.Release(resumed) })
	require.NoError(t, sessionlock.Hold(crashed))
	sessionlock.Release(crashed)

	list, err := NewFrontendPort(FrontendDeps{WorkDir: projectDir}).ListSessions(context.Background())
	require.NoError(t, err)
	state := map[string]string{}
	for _, s := range list {
		state[s.Harp] = s.State
		assert.Equal(t, "claude-code", s.Engine)
	}
	assert.Equal(t, frontend.SessionLive, state[resumed], "a held lock is a running session, whatever ended_at says")
	assert.Equal(t, frontend.SessionEnded, state[crashed], "a free lock is the end of the session, whether or not ended_at was written")
	assert.Equal(t, frontend.SessionUnknown, state[unlocked], "no ended_at is not evidence of life")
	assert.Equal(t, frontend.SessionUnknown, state[unlockedEnded], "an ended_at is not evidence of death")
}

// BenchmarkFrontendPort_ListSessionsProbesEveryIndexRow is the cost of the
// ruling that a roster render asks the lock once per index row, at the size
// of a real project index. The rows are a third each of held, free and
// never-locked, so all three probe paths are paid.
func BenchmarkFrontendPort_ListSessionsProbesEveryIndexRow(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	projectDir := home + "/proj"
	mgr, err := sessions.Open(nil)
	require.NoError(b, err)
	const n = 650
	for i := range n {
		e, err := mgr.AssignHarp(projectDir, "claude-code")
		require.NoError(b, err)
		switch i % 3 {
		case 0:
			require.NoError(b, sessionlock.Hold(e.HarpName))
			b.Cleanup(func() { sessionlock.Release(e.HarpName) })
		case 1:
			require.NoError(b, sessionlock.Hold(e.HarpName))
			sessionlock.Release(e.HarpName)
		}
	}
	port := NewFrontendPort(FrontendDeps{WorkDir: projectDir})
	b.ResetTimer()
	for range b.N {
		_, _ = port.ListSessions(context.Background())
	}
}
