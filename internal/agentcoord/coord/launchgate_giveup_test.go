package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Budget
// exhaustion used to be announced ONLY for CauseLaunchFailed, so an
// attach-then-die loop ended in total silence with the child's mail still
// queued — a stranded mailbox nobody is told about. relaunchForLeftoverMail now
// calls giveUpLaunching whenever the budget is what refused, whatever burned it,
// and only a `stopped` refusal stays silent (that one is the operator's own
// agent_stop being honoured).
//
// The assertion is on the PARENT'S MAILBOX, not on stderr: the parent is an
// agent whose only input is its mailbox, and the coordinator's stderr is a
// channel it cannot read.
func TestGiveUpLaunching_NotifiesTheParentForANonLaunchCause(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const (
		harp   = "spin-harp"
		parent = "parent-harp"
	)
	// Mail waiting for a harp whose budget is spent: the exact stranded state.
	if _, _, err := c.queueMail(parent, harp, "task", "please do the thing"); !assert.NoError(t, err) {
		return
	}
	simulateAttachThenDie(c, harp, 200)
	if _, ok, exhausted := c.nextRelaunch(harp); !assert.False(t, ok) || !assert.True(t, exhausted,
		"precondition: the budget must be exhausted, not stopped") {
		return
	}

	rec := RunRecord{RunID: "run-1", Harp: harp, Agent: "worker", ParentHarp: parent, Ended: true, Cause: CauseRunnerExit}
	c.relaunchForLeftoverMail(rec, CauseRunnerExit, "engine exited 1")

	msgs, err := c.recvMail(context.Background(), parent, 2*time.Second)
	if !assert.NoError(t, err, "the parent must be told its child's launcher gave up") {
		return
	}
	var body string
	for _, m := range msgs {
		if m.Kind == "error" {
			body = m.Body
		}
	}
	if !assert.NotEmpty(t, body, "an exhausted budget must reach the parent's mailbox as an error, whatever burned it") {
		return
	}
	assert.Contains(t, body, "without consuming any of its mail",
		"the notice must describe the attach-then-die shape, not a launch failure")
	assert.Contains(t, body, "still waiting", "and say that the child's mail is stranded")
}

// TestGiveUpLaunching_StaysSilentForAnOperatorStop is F04's other half: a
// refusal caused by `stopped` is the operator's own agent_stop being honoured
// and must NOT mail the parent. Without this the "be loud whatever burned it"
// fix would turn every agent_stop into an error report.
func TestGiveUpLaunching_StaysSilentForAnOperatorStop(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const (
		harp   = "stopped-harp"
		parent = "parent-harp"
	)
	if _, _, err := c.queueMail(parent, harp, "task", "please do the thing"); !assert.NoError(t, err) {
		return
	}
	c.cancelLaunch(harp) // an explicit agent_stop

	rec := RunRecord{RunID: "run-1", Harp: harp, Agent: "worker", ParentHarp: parent, Ended: true, Cause: CauseRunnerExit}
	c.relaunchForLeftoverMail(rec, CauseRunnerExit, "engine exited 1")

	msgs, err := c.recvMail(context.Background(), parent, 200*time.Millisecond)
	if err == nil {
		for _, m := range msgs {
			assert.NotEqual(t, "error", m.Kind, "a stopped harp's refusal is the operator's own decision: no report is due")
		}
	}
}

// The register's
// claim was that `agent_stop` "refuses resumed children" — under one-shot a
// healthy child spends the gap between turns with an ENDED current run, so
// AgentStop takes the rec.Ended branch, and BEFORE the fix that branch returned
// without touching the launch gate: the stop reported something and stopped
// nothing, while a relaunch armed behind it minted a fresh run and carried on.
//
// AgentStop now calls cancelLaunch BEFORE the Ended check, on both paths, so a
// stop that lands on an ended run still marks the harp stopped. That is the
// load-bearing part: not the message, but that no relaunch may follow.
func TestAgentStop_OnAnEndedRunStillStopsTheRelaunch(t *testing.T) {
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "hello", "", "")
	if !assert.NoError(t, err) {
		return
	}
	// The child's current run ends — the one-shot between-turns state, and also
	// the retry loop's own terminal.
	c.terminateRun(out.RunID, CauseRunnerExit, "engine exited between turns")

	msg, err := c.AgentStop(ownerIdentity(), out.Harp)
	if !assert.NoError(t, err, "a stop landing on an ended run must not be an error — the child is still stoppable") {
		return
	}
	assert.Contains(t, msg, "relaunch is cancelled",
		"the operator must be told the stop actually took effect on the launcher")

	assert.True(t, c.launchStopped(out.Harp), "the harp must be marked stopped even though the run had ended")
	delay, ok, exhausted := c.nextRelaunch(out.Harp)
	assert.False(t, ok, "no relaunch may be authorised behind an agent_stop")
	assert.False(t, exhausted, "and the refusal is the stop, not an exhausted budget")
	assert.Zero(t, delay)
}
