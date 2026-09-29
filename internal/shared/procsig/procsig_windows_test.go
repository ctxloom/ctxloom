//go:build windows

package procsig

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStop_EndsTheProcess: on Windows Stop is a kill — os.Process.Signal
// delivers nothing a process could honour — so the reporter never gets to
// name a signal; it is simply gone.
func TestStop_EndsTheProcess(t *testing.T) {
	cmd, _ := spawnReporter(t)
	require.NoError(t, Stop(cmd.Process))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.Error(t, err, "a killed process does not exit 0")
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not end the process")
	}
}
