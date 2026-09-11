package mcp

import (
	"errors"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// What a timed-out agent_recv tells its caller to do next, by audience. The
// coord sentinel is deliberately audience-neutral (it sees a role, not who is
// parked), so the instruction is attached here, where both surfaces know
// whether they serve a leaf. The split exists because a coordinator handed the
// child's instruction on a quiet wait was observed to obey it and finish with
// children still running.
const (
	recvTimeoutLeafGuidance        = "drop the coordination: write your report/deferral state and finish"
	recvTimeoutCoordinatorGuidance = "nothing arrived in this window; if you are still waiting on children, receive again"
)

// recvFailure renders a receive's error for the caller: a timeout gains the
// elapsed wait and this audience's guidance, wrapped so errors.Is still
// matches the sentinel; anything else passes through untouched.
func recvFailure(err error, wait time.Duration, leaf bool) error {
	if !errors.Is(err, coord.ErrRecvTimeout) {
		return err
	}
	guidance := recvTimeoutCoordinatorGuidance
	if leaf {
		guidance = recvTimeoutLeafGuidance
	}
	return fmt.Errorf("%w (waited %s); %s", coord.ErrRecvTimeout, wait, guidance)
}
