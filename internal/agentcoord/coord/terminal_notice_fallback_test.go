package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminateRun's synthesized terminal notice is the sole guarantee behind the
// invariant "the parent ALWAYS learns of a child death" — yet a queueMail
// journal failure reduced it to a stderr clidiag.Warn the parent (an agent
// whose only input channel is its mailbox / its parked agent_recv) can never
// read. A parent parked in agent_recv when its child dies then blocks forever,
// because terminateRun severs the CHILD's poll (rec.Harp) but never touches the
// parent's. A closed mail journal is the deterministic shape of that failure.
func TestTerminateRun_QueueMailFailure_ParkedParentIsUnblocked(t *testing.T) {
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	parent := ownerIdentity()
	// enqueueRun alone gives us a live, non-ended RunRecord whose ParentHarp is
	// the owner — no launch, no engine, so the terminal is the only event.
	rt, _, err := c.enqueueRun(parent, &SpawnPlan{AgentName: "worker"}, "child-x", "task", false, make(chan struct{}), 1)
	require.NoError(t, err)

	type recvResult struct {
		msgs []Message
		err  error
	}
	done := make(chan recvResult, 1)
	go func() {
		// A long wait: if the parent is not actively unblocked it hangs here,
		// which the select below catches as the defect.
		msgs, rerr := c.AgentRecv(context.Background(), parent, 30*time.Second)
		done <- recvResult{msgs, rerr}
	}()

	// Wait until the parent's long-poll is genuinely parked.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		p := c.polls[parent.Harp]
		return p != nil && !p.done
	}, 2*time.Second, 5*time.Millisecond, "parent never parked in agent_recv")

	// Break the mailbox journal so the terminal notice's durable queueMail fails.
	require.NoError(t, c.mail.Close())

	// The child dies. Even though the durable queue now fails, the parent must
	// still learn — it is parked and must be unblocked with a death signal.
	c.terminateRun(rt.runID, CauseRunnerLoss, "docker stop")

	select {
	case r := <-done:
		require.NoError(t, r.err,
			"a parent parked in agent_recv must be unblocked with the death notice, not left to time out")
		require.NotEmpty(t, r.msgs,
			"the unblocked parent must observe the child's death, not an empty wake")
		found := false
		for _, m := range r.msgs {
			if m.From == "child-x" && (m.Kind == KindExited || m.Kind == "error") {
				found = true
			}
		}
		assert.True(t, found,
			"the unblocked parent observes an exited/error notice from the dead child (got %+v)", r.msgs)
	case <-time.After(5 * time.Second):
		t.Fatal("parent blocked in agent_recv after its child died and the terminal notice's queueMail failed — " +
			"the 'parent ALWAYS learns of a child death' invariant was silently violated")
	}
}
