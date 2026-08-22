package coord

import (
	"sync"
	"testing"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestHandleChildEvent_OneshotReadIsSynchronized drives the two real
// production methods that touch childRt.oneshot against each other:
// attachLaunch WRITES it under c.mu, handleChildEvent READS it to decide
// whether a non-assistant entry counts as turn output. The read used to sit
// OUTSIDE the mutex — the very next statement inside the branch takes
// c.mu.Lock() — so the two accesses had no happens-before edge at all.
//
// This test's assertion is the -race detector: a detected race fails the
// test. Nothing else here can go red, which is the point — the defect is
// invisible to any value assertion (both branches produce plausible output).
func TestHandleChildEvent_OneshotReadIsSynchronized(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	rt := &childRt{runID: "run-oneshot-race", harp: "child-oneshot-race", wake: make(chan struct{}, 1)}

	const iterations = 2000
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			c.attachLaunch(rt, &operations.AgentChatLaunch{Oneshot: i%2 == 0})
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			c.handleChildEvent(rt, agent.ChatEvent{
				Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, Content: "chatter"},
			})
		}
	}()
	close(start)
	wg.Wait()
}
