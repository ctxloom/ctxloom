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
	// open; past this the caller is expected to give up and finish.
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
