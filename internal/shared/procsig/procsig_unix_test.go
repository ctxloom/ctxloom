//go:build !windows

package procsig

import (
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStop_DeliversSIGTERM: on unix Stop is the orderly-teardown request, a
// SIGTERM the child's signal context unwinds on — not an interrupt, and not a
// kill it could never observe.
func TestStop_DeliversSIGTERM(t *testing.T) {
	cmd, r := spawnReporter(t)
	require.NoError(t, Stop(cmd.Process))
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "got "+syscall.SIGTERM.String(), strings.TrimSpace(line))
	require.NoError(t, cmd.Wait())
}

// TestSpawnAttr_LeadsItsOwnProcessGroup: a child spawned with SpawnAttr leads
// a process group of its own, so a signal aimed at the caller's group (a ^C
// on a terminal the caller shares) never reaches it behind the caller's back —
// Interrupt is the only way it is asked to stop.
func TestSpawnAttr_LeadsItsOwnProcessGroup(t *testing.T) {
	cmd, _ := spawnReporter(t)
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	require.NoError(t, err)
	assert.Equal(t, cmd.Process.Pid, pgid)
	assert.NotEqual(t, syscall.Getpgrp(), pgid)
}
