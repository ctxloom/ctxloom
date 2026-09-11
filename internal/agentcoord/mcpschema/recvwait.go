package mcpschema

import (
	"fmt"
	"time"
)

// The agent_recv `wait` contract, advertised on two tool surfaces (the runner's
// generated schema, the stdio server's typed input) and enforced by two
// handlers. The numbers and the prose quoting them are ONE declaration: a model
// reads the advertised maximum and plans around it, so a clamp that disagrees
// with the text cuts a caller off at a boundary it was told it could ask for.
const (
	// RecvWaitDefault is what an absent, zero or negative wait resolves to.
	// Never zero: that turns a park into a poll, and a polling child burns its
	// execution slot instead of yielding it.
	RecvWaitDefault = 60 * time.Second
	// RecvWaitMax caps one recv's park. A parked child holds a coordination
	// open; past this a child is expected to give up and finish, and a
	// coordinator to decide whether to receive again.
	RecvWaitMax = 10 * time.Minute
)

// RecvWaitDoc is the advertised description of the wait parameter, quoting the
// bounds above so the text cannot drift from what ClampRecvWait enforces.
var RecvWaitDoc = fmt.Sprintf(
	"Seconds to wait for a message (default %d, max %d); on timeout a coordinator gets a successful empty result with a disposition, a leaf an error",
	int(RecvWaitDefault.Seconds()), int(RecvWaitMax.Seconds()))

// ClampRecvWait resolves a caller-supplied wait in SECONDS to the duration a
// recv handler parks for: absent/zero/negative takes the default, anything past
// the maximum is clamped to it.
func ClampRecvWait(seconds int) time.Duration {
	wait := time.Duration(seconds) * time.Second
	if wait <= 0 {
		return RecvWaitDefault
	}
	if wait > RecvWaitMax {
		return RecvWaitMax
	}
	return wait
}

// RecvDispositionYielded is agent_recv's `disposition` when the call completed
// by YIELDING: one receive is live per session, a newer one supersedes an
// older parked one, and the superseded call returns successfully with no
// messages and this text. It is prose, not a code, because the caller is a
// model deciding what to do next — and the one thing it must not do is retry,
// which would supersede the receive that is about to deliver. Mail is never
// lost across the yield: the newer receive holds the park.
const RecvDispositionYielded = "yielded to a newer receive for this session: no message was lost (the newer receive holds the park and delivers whatever lands) — do not retry this call"

// RecvDispositionTimedOut is agent_recv's `disposition` when a COORDINATOR's
// wait elapsed with nothing to deliver. Only a coordinator gets it: for a leaf
// the same timeout stays an error, because a leaf's quiet wait is its signal
// to stop and its harness should show red. A coordinator's is not — rendered
// as a failed call it is the same misleading red the yielded disposition
// exists to prevent, and the caller retries into it. The window's length is
// the caller's own `wait`, so it is not restated here.
const RecvDispositionTimedOut = "timed out with no message: nothing arrived in this window; if you are still waiting on children, receive again"
