package interaction

import (
	"errors"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// What a timed-out agent_recv tells a LEAF to do next. The coord sentinel is
// deliberately audience-neutral (it sees a role, not who is parked), so the
// instruction is attached here, where both surfaces know they serve a leaf.
// A coordinator never reads this text — its timeout is not an error at all
// (see RecvOutcome) — because a coordinator handed the child's instruction on
// a quiet wait was observed to obey it and finish with children still running.
const RecvTimeoutLeafGuidance = "drop the coordination: write your report/deferral state and finish"

// RecvOutcome classifies a receive that returned err, for both MCP surfaces.
// A non-nil failure is what the caller returns as the tool error; otherwise
// the call is a SUCCESS with no messages and the returned disposition.
//
// The verdict shape depends on ROLE, on purpose, and the two must not be
// unified: a LEAF's timeout stays an error (it should stop, and its harness
// should show red), while a COORDINATOR's timeout is a successful empty
// receive (mcpschema.RecvDispositionTimedOut) — "nothing arrived; receive
// again" rendered as a failed call is the same misleading red that drove the
// retry loop the yielded disposition cured. A yield is a success for every
// audience. The role signal is the caller's: the runner's leaf flag and the
// stdio identity's IsChild, never the transport.
func RecvOutcome(err error, wait time.Duration, leaf bool) (disposition string, failure error) {
	switch {
	case errors.Is(err, coord.ErrRecvPreempted):
		return mcpschema.RecvDispositionYielded, nil
	case !errors.Is(err, coord.ErrRecvTimeout):
		return "", err
	case leaf:
		return "", fmt.Errorf("%w (waited %s); %s", coord.ErrRecvTimeout, wait, RecvTimeoutLeafGuidance)
	}
	return mcpschema.RecvDispositionTimedOut, nil
}
