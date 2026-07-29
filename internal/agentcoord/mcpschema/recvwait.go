package mcpschema

import (
	"fmt"
	"time"
)

// The agent_recv `wait` contract. It is advertised on two tool surfaces (the
// runner's generated schema and the stdio server's typed input) and enforced by
// two handlers, so it lives here — the package both surfaces already compile
// against — rather than being restated at each site.
//
// The numbers and the prose that quotes them are ONE declaration on purpose: a
// model reads the advertised maximum and plans around it, so a clamp that
// disagrees with the text cuts a caller off at a boundary it was told it could
// ask for, and reports it as a timeout.
const (
	// RecvWaitDefault is the park duration an absent, zero or negative wait
	// resolves to. Never zero: a zero wait turns a park into a poll, and a child
	// polling its mailbox burns its execution slot instead of yielding it.
	RecvWaitDefault = 60 * time.Second
	// RecvWaitMax caps how long one recv may park. A parked child holds a
	// coordination open; past this the caller is expected to give up, write its
	// report or deferral state, and finish.
	RecvWaitMax = 10 * time.Minute
)

// RecvWaitDoc is the advertised description of the wait parameter, quoting the
// bounds above so the text cannot drift from what ClampRecvWait enforces.
var RecvWaitDoc = fmt.Sprintf(
	"Seconds to wait for a message (default %d, max %d). On timeout the call fails: drop the coordination, write your report/deferral state, and finish",
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
