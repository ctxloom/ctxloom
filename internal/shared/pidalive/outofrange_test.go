package pidalive

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProbe_OutOfRangePidIsUnsure pins the contract for a pid that cannot name
// a single process. Both platform arms must answer the same way, so this test
// carries no build tag: Unix used to reinterpret the argument as a process
// group while Windows rejected it, which meant one persisted-and-corrupted pid
// produced two different verdicts depending on where ctxloom happened to run.
func TestProbe_OutOfRangePidIsUnsure(t *testing.T) {
	for _, pid := range []int{0, -1, -5} {
		assert.Equal(t, Unsure, Probe(pid),
			"a pid that names no single process must be Unsure, never a confident verdict (pid %d)", pid)
	}
}
