//go:build !windows

package pidalive

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbe_NegativePidIsNotAProcessGroupProbe demonstrates WHY the guard
// exists rather than only that it is there. A negative pid is a live, fully
// signalable target as far as kill(2) is concerned — it just is not a process.
// Without the guard the probe would hand back a confident Alive for it.
func TestProbe_NegativePidIsNotAProcessGroupProbe(t *testing.T) {
	pgid, err := syscall.Getpgid(0)
	require.NoError(t, err)
	require.Positive(t, pgid)

	// Fixture check: the kernel really does accept -pgid as a live target. If
	// it did not, the assertion below would pass for the wrong reason — a
	// probe that answered "not alive" because there was nothing there.
	require.NoError(t, syscall.Kill(-pgid, 0),
		"the fixture process group is not signalable; there is nothing hostile to guard against")

	assert.Equal(t, Unsure, Probe(-pgid),
		"probing -pgid must not be answered as if it were a process: kill(2) would report the whole GROUP alive")
}
