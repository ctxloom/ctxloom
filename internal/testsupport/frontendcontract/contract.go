// Package frontendcontract is the frontend port's contract: what a frontend
// can observe and do through frontend.Port, written once and run against
// every implementation. An implementation's suite supplies a World — a live
// session it can drive from outside the port — and Run asserts the port's
// behaviour against it.
package frontendcontract

import (
	"bufio"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// Engine facts every World's scripted engine reports, which the contract
// asserts arrive through the port unchanged.
const (
	// ContextTokens is the context usage each child turn's completion reports.
	ContextTokens = 10
	// ContextWindow is the context window each child turn's completion
	// reports.
	ContextWindow = 1000
	// AnswerPrefix starts every child turn's answer: "echo: " + its prompt.
	AnswerPrefix = "echo: "
	// TerminalReady is the line the session's interactive engine prints
	// first; then, for each line typed into it, "got:<line>" and its window
	// size as "<rows> <cols>".
	TerminalReady = "ready"
)

// World is one live session the contract drives: a coordinator holding
// delegated children whose engine is scripted, and the session's own
// interactive terminal.
type World interface {
	// Port is the port under test.
	Port() frontend.Port
	// Self is the session's own harp: the coordinator's owner, and the run
	// with the interactive terminal.
	Self() string
	// Delegate starts a delegated child (agent "worker") on prompt, and
	// returns its harp once its first turn has completed.
	Delegate(t *testing.T, prompt string) string
	// Prompts is every turn text harp's engine has been handed so far.
	Prompts(harp string) []string
	// Ask has harp's run ask the root human to allow tool, as its engine's
	// permission prompt would; the decision the asker receives arrives on
	// the channel.
	Ask(t *testing.T, harp, tool string) <-chan coord.ApprovalDecision
	// FailNextTurn makes harp's next turn be turned away with kind.
	FailNextTurn(harp string, kind agent.FailureKind)
	// Stop ends harp's run as its parent's agent_stop does, returning once
	// the run has ended.
	Stop(t *testing.T, harp string)
}

// Run asserts the port's contract. live builds a fresh World per case;
// degraded builds the port of a session that hosts no coordinator and no
// terminal.
func Run(t *testing.T, live func(t *testing.T) World, degraded func(t *testing.T) frontend.Port) {
	t.Run("runs: a delegated child is listed with its lineage, state and context usage", func(t *testing.T) { runsList(t, live(t)) })
	t.Run("runs: an ended child stays listed with why it ended", func(t *testing.T) { runsEnded(t, live(t)) })
	t.Run("runs: a watch opens on a snapshot, streams live events, and closes with its context", func(t *testing.T) { runsWatch(t, live(t)) })
	t.Run("sessions: the index lists a delegated child with a liveness verdict", func(t *testing.T) { sessionsList(t, live(t)) })
	t.Run("feed: a child's answers reach its feed live", func(t *testing.T) { feedLive(t, live(t)) })
	t.Run("approvals: a request parks, is answered once, and its session grant can be revoked", func(t *testing.T) { approvalsAnswer(t, live(t)) })
	t.Run("approvals: a request denied with a message tells the asker why", func(t *testing.T) { approvalsDeny(t, live(t)) })
	t.Run("holds: a turn turned away parks its run on a hold the port lists", func(t *testing.T) { holdsList(t, live(t)) })
	t.Run("control: the human's steer is the child's next turn", func(t *testing.T) { controlSteer(t, live(t)) })
	t.Run("control: pause and resume show as the run's hold", func(t *testing.T) { controlPause(t, live(t)) })
	t.Run("control: a harp the coordinator does not hold is refused", func(t *testing.T) { controlUnknown(t, live(t)) })
	t.Run("terminal: the session's own run carries keystrokes, output and resize", func(t *testing.T) { terminalAttach(t, live(t)) })
	t.Run("terminal: a delegated child has none", func(t *testing.T) { terminalChild(t, live(t)) })
	t.Run("degraded: a session with no coordinator observes nothing and is refused control", func(t *testing.T) { degradedPort(t, degraded(t)) })
}

// wait is the contract's bound on anything that crosses the live session.
const wait = 30 * time.Second

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, wait, 10*time.Millisecond, what)
}

// runOf is harp's row in the port's ListRuns, or false.
func runOf(t *testing.T, p frontend.Port, harp string) (coord.RunInfo, bool) {
	t.Helper()
	snap, err := p.ListRuns(context.Background())
	require.NoError(t, err)
	for _, r := range snap.Runs {
		if r.Agent != nil && r.Agent.AgentID == harp {
			return r, true
		}
	}
	return coord.RunInfo{}, false
}

func runsList(t *testing.T, w World) {
	child := w.Delegate(t, "list me")
	eventually(t, "the child's row carries its turn's context usage", func() bool {
		r, ok := runOf(t, w.Port(), child)
		return ok && r.ContextTokens == ContextTokens
	})
	r, _ := runOf(t, w.Port(), child)
	assert.Equal(t, "worker", r.Agent.Role, "the row names the child's agent")
	assert.Equal(t, w.Self(), r.ParentHarp, "the row names the harp that delegated it")
	assert.Contains(t, []string{coord.StateExecuting, coord.StateIdle}, r.Phase, "a child that answered is live")
	assert.False(t, r.LastEventAt.IsZero(), "the row says when the child last moved")
	assert.NotEmpty(t, r.RunID)
	assert.Equal(t, uint64(ContextWindow), r.ContextWindow, "the row carries the window its usage is out of")
}

func runsEnded(t *testing.T, w World) {
	child := w.Delegate(t, "end me")
	w.Stop(t, child)
	r, ok := runOf(t, w.Port(), child)
	require.True(t, ok, "an ended child is still listed")
	assert.Equal(t, coord.StateEnded, r.Phase)
	assert.Equal(t, coord.CauseStopped, r.Cause, "the row says why it ended")
}

func runsWatch(t *testing.T, w World) {
	child := w.Delegate(t, "watch me")
	row, ok := runOf(t, w.Port(), child)
	require.True(t, ok)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch, err := w.Port().WatchRuns(ctx, nil)
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(watch.Snapshot.Runs, func(r coord.RunInfo) bool { return r.RunID == row.RunID }),
		"the snapshot holds the run the roster lists")

	_, err = w.Port().Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: child, Body: "stream this"})
	require.NoError(t, err)
	var sawAnswer bool
	deadline := time.After(wait)
	for !sawAnswer {
		select {
		case ev, open := <-watch.Events:
			require.True(t, open, "the stream closed while its context was live")
			if ev.RunID != row.RunID {
				continue
			}
			switch pl := ev.Payload.(type) {
			case coord.MessageDelta:
				sawAnswer = strings.Contains(pl.Text, "stream this")
			case coord.MessageCompleted:
				sawAnswer = strings.Contains(pl.FullText, "stream this")
			}
		case <-deadline:
			t.Fatal("the steered turn's answer never arrived on the watch")
		}
	}
	cancel()
	closed := time.After(wait)
	for {
		select {
		case _, open := <-watch.Events:
			if !open {
				return
			}
		case <-closed:
			t.Fatal("the stream did not close once its context ended")
		}
	}
}

func sessionsList(t *testing.T, w World) {
	child := w.Delegate(t, "index me")
	list, err := w.Port().ListSessions(context.Background())
	require.NoError(t, err)
	i := slices.IndexFunc(list, func(s frontend.Session) bool { return s.Harp == child })
	require.GreaterOrEqual(t, i, 0, "the index lists the delegated child: %+v", list)
	assert.Contains(t, []string{frontend.SessionLive, frontend.SessionEnded, frontend.SessionUnknown}, list[i].State)
	assert.NotEmpty(t, list[i].Engine, "the index names the child's engine")
}

func feedLive(t *testing.T, w World) {
	child := w.Delegate(t, "feed me")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed, err := w.Port().WatchFeed(ctx, child)
	require.NoError(t, err)
	assert.Equal(t, "live", feed.Source, "the coordinator holds the child, so its feed is the live stream")
	_, err = w.Port().Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: child, Body: "feed this"})
	require.NoError(t, err)
	deadline := time.After(wait)
	for {
		select {
		case ev, open := <-feed.Events:
			require.True(t, open, "the feed ended before the child answered")
			if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant && strings.Contains(ev.Entry.Content, "feed this") {
				assert.True(t, strings.HasPrefix(ev.Entry.Content, AnswerPrefix))
				return
			}
		case <-deadline:
			t.Fatal("the child's answer never reached its feed")
		}
	}
}

func approvalsAnswer(t *testing.T, w World) {
	child := w.Delegate(t, "ask me")
	src := w.Port().Approvals()
	require.NotNil(t, src, "a session with a coordinator has an approval queue")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := src.Subscribe(ctx)

	decided := w.Ask(t, child, "Bash")
	var p coord.PendingApproval
	eventually(t, "the request parks in the root's queue", func() bool {
		for _, q := range src.Pending() {
			if q.From.Harp == child {
				p = q
				return true
			}
		}
		return false
	})
	assert.Equal(t, "Bash", p.Ask.Tool)
	assert.Equal(t, []string{w.Self(), child}, p.Lineage, "the request names its asker's lineage, root first")
	assert.Equal(t, "worker", p.Agent)
	requireQueueEvent(t, events, coord.QueueAdded, p.ID)

	const rule = "Bash(ls:*)"
	require.NoError(t, src.Answer(p.ID, coord.ApprovalDecision{Allow: true, SessionRules: []string{rule}}))
	select {
	case d := <-decided:
		assert.True(t, d.Allow, "the asker is allowed")
		assert.Equal(t, agent.DeciderHuman, d.Decider, "the queue, not the frontend, names who decided")
	case <-time.After(wait):
		t.Fatal("the asker never received the decision")
	}
	requireQueueEvent(t, events, coord.QueueResolved, p.ID)
	assert.ErrorIs(t, src.Answer(p.ID, coord.ApprovalDecision{Allow: true}), coord.ErrApprovalResolved, "a request is answered once")
	assert.ErrorIs(t, src.Answer("apv-never-issued", coord.ApprovalDecision{}), coord.ErrNoSuchApproval)

	var grant coord.Grant
	eventually(t, "the session grant is listed for the child", func() bool {
		gs := src.Grants(child)
		if i := slices.IndexFunc(gs, func(g coord.Grant) bool { return g.Rule == rule }); i >= 0 {
			grant = gs[i]
			return true
		}
		return false
	})
	assert.Equal(t, p.ID, grant.From, "the grant names the request it came from")
	require.NoError(t, src.Revoke(child, grant.ID))
	assert.Empty(t, src.Grants(child), "a revoked grant is gone")
	assert.ErrorIs(t, src.Revoke(child, grant.ID), coord.ErrNoSuchGrant)
}

func approvalsDeny(t *testing.T, w World) {
	child := w.Delegate(t, "deny me")
	src := w.Port().Approvals()
	require.NotNil(t, src)
	decided := w.Ask(t, child, "Write")
	var id coord.ApprovalID
	eventually(t, "the request parks", func() bool {
		for _, q := range src.Pending() {
			if q.From.Harp == child {
				id = q.ID
				return true
			}
		}
		return false
	})
	require.NoError(t, src.Answer(id, coord.ApprovalDecision{Message: "not that file"}))
	select {
	case d := <-decided:
		assert.False(t, d.Allow)
		assert.Equal(t, "not that file", d.Message, "the asker is told why")
	case <-time.After(wait):
		t.Fatal("the asker never received the denial")
	}
	assert.Empty(t, src.Grants(child), "a denial grants nothing")
}

func requireQueueEvent(t *testing.T, events <-chan coord.QueueEvent, kind coord.QueueEventKind, id coord.ApprovalID) {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case ev := <-events:
			if ev.Kind == kind && ev.ID == id {
				return
			}
		case <-deadline:
			t.Fatalf("no queue event %v for %s", kind, id)
		}
	}
}

func holdsList(t *testing.T, w World) {
	child := w.Delegate(t, "hold me")
	w.FailNextTurn(child, agent.FailureOverloaded)
	_, err := w.Port().Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: child, Body: "overload this"})
	require.NoError(t, err)
	var hold coord.CredentialHold
	eventually(t, "the port lists the hold the overload opened", func() bool {
		holds, err := w.Port().CredentialHolds(context.Background())
		require.NoError(t, err)
		for _, h := range holds {
			if slices.Contains(h.Harps, child) {
				hold = h
				return true
			}
		}
		return false
	})
	assert.Equal(t, agent.FailureOverloaded, hold.Kind)
	assert.True(t, hold.Until.After(hold.Since), "an overload backs off until a deadline")
	r, ok := runOf(t, w.Port(), child)
	require.True(t, ok)
	require.NotNil(t, r.Hold, "the held run's row carries its hold")
	assert.Equal(t, string(agent.FailureOverloaded), r.Hold.Kind)
}

func controlSteer(t *testing.T, w World) {
	child := w.Delegate(t, "steer me")
	res, err := w.Port().Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: child, Body: "now do this"})
	require.NoError(t, err)
	assert.Equal(t, coord.ControlVerbSteer, res.Verb)
	assert.NotEmpty(t, res.MessageID, "a steer is durable mail with an id")
	eventually(t, "the steer is handed to the child's engine as a turn", func() bool {
		return slices.ContainsFunc(w.Prompts(child), func(p string) bool { return strings.Contains(p, "now do this") })
	})
}

func controlPause(t *testing.T, w World) {
	child := w.Delegate(t, "pause me")
	res, err := w.Port().Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbPause, Harp: child, Body: "looking"})
	require.NoError(t, err)
	assert.True(t, res.Changed, "this request paused it")
	r, ok := runOf(t, w.Port(), child)
	require.True(t, ok)
	require.NotNil(t, r.Hold, "a paused run shows its pause as its hold")
	assert.Equal(t, coord.HoldKindHuman, r.Hold.Kind, "the human paused it")

	res, err = w.Port().Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbResume, Harp: child})
	require.NoError(t, err)
	assert.True(t, res.Changed, "this request resumed it")
	r, _ = runOf(t, w.Port(), child)
	assert.Nil(t, r.Hold, "a resumed run is held by nothing")
}

func controlUnknown(t *testing.T, w World) {
	_, err := w.Port().Control(context.Background(), coord.ControlRequest{Verb: coord.ControlVerbPause, Harp: "no-such-harp"})
	assert.ErrorIs(t, err, coord.ErrNotInjectable)
}

func terminalAttach(t *testing.T, w World) {
	term, err := w.Port().Terminal(w.Self())
	require.NoError(t, err)
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(term)
		for sc.Scan() {
			lines <- strings.TrimRight(sc.Text(), "\r")
		}
		close(lines)
	}()
	expectLine(t, lines, TerminalReady)
	require.NoError(t, term.Resize(30, 100))
	_, err = term.Write([]byte("hi\n"))
	require.NoError(t, err)
	expectLine(t, lines, "got:hi")
	expectLine(t, lines, "30 100")
}

func expectLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(wait)
	var seen []string
	for {
		select {
		case l, open := <-lines:
			if !open {
				t.Fatalf("the terminal ended before %q (saw %q)", want, seen)
			}
			if l == want {
				return
			}
			seen = append(seen, l)
		case <-deadline:
			t.Fatalf("the terminal never printed %q (saw %q)", want, seen)
		}
	}
}

func terminalChild(t *testing.T, w World) {
	child := w.Delegate(t, "no tty")
	_, err := w.Port().Terminal(child)
	assert.ErrorIs(t, err, frontend.ErrNoTerminal)
}

func degradedPort(t *testing.T, p frontend.Port) {
	ctx := context.Background()
	snap, err := p.ListRuns(ctx)
	require.NoError(t, err)
	assert.Empty(t, snap.Runs, "no coordinator holds no runs")
	holds, err := p.CredentialHolds(ctx)
	require.NoError(t, err)
	assert.Empty(t, holds)
	assert.Nil(t, p.Approvals(), "no coordinator hosts no approval queue")
	_, err = p.WatchRuns(ctx, nil)
	assert.ErrorIs(t, err, frontend.ErrNoCoordinator)
	_, err = p.Control(ctx, coord.ControlRequest{Verb: coord.ControlVerbPause, Harp: "any"})
	assert.ErrorIs(t, err, coord.ErrNotInjectable)
	_, err = p.Terminal("any")
	assert.True(t, errors.Is(err, frontend.ErrNoTerminal))
	_, err = p.ListSessions(ctx)
	assert.NoError(t, err, "the session index needs no coordinator")
}
