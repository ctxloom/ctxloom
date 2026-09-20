package coord

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHome_RedialPreemptsThePendingBackoff pins the product's redial bound:
// a Home whose coordinator is unreachable waits homeRedialBackoff between
// attempts, and Redial makes the NEXT attempt happen now. Forced against a
// listener that refuses every connection (each accept is one dial
// attempt): after the first attempts have been refused and the loops are
// in their backoff, a kick must produce further attempts well inside the
// backoff — a restarted coordinator's re-adoption test drives the redial
// this way instead of waiting on the timer.
func TestHome_RedialPreemptsThePendingBackoff(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var accepts atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_ = conn.Close() // refused: the loop goes into its backoff
		}
	}()

	h, err := NewHome(context.Background(), HomeConfig{
		Reporter: termSink(),
		URL:      fmt.Sprintf("http://%s/mcp", ln.Addr().String()),
		Token:    "t", RunID: "run-1", Harness: "mock", Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(h.Crash)

	// Both loops (RunnerChannel, RunChannel) have dialed once and been refused.
	require.Eventually(t, func() bool { return accepts.Load() >= 2 }, conformanceWait, 5*time.Millisecond)
	before := accepts.Load()

	h.Redial()
	require.Eventually(t, func() bool { return accepts.Load() > before }, homeRedialBackoff/4, 5*time.Millisecond,
		"a kicked Home must redial now, not after homeRedialBackoff (%s)", homeRedialBackoff)
}
