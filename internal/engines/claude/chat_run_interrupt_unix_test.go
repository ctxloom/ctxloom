//go:build !windows

package claude

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readLine reads one line off the transport's stdout, bounded so a process
// that never says it fails the test instead of hanging it.
func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		line, _ := r.ReadString('\n')
		got <- strings.TrimSpace(line)
	}()
	select {
	case line := <-got:
		return line
	case <-time.After(10 * time.Second):
		t.Fatal("the process said nothing")
		return ""
	}
}

// TestSpawnChatTransport_CancelInterruptsTheProcess: ending the turn's context
// INTERRUPTS the engine process (procsig.Interrupt) — it is not killed — so a
// claude that unwinds on SIGINT keeps its session for the next turn's
// --resume. The process also leads its own process group (procsig.SpawnAttr).
func TestSpawnChatTransport_CancelInterruptsTheProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The trap is armed before the pid line is written, so the cancel below
	// can only land on an armed handler.
	script := `trap 'echo interrupted; exit 0' INT; echo "$$"; while :; do sleep 0.05; done`
	grace := 5 * time.Second
	tr, err := spawnChatTransportGrace(ctx, "sh", []string{"-c", script}, nil, t.TempDir(), grace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Close() })
	r := bufio.NewReader(tr.stdout)

	pid, err := strconv.Atoi(readLine(t, r))
	require.NoError(t, err)
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	assert.Equal(t, pid, pgid, "the engine process leads its own process group")

	start := time.Now()
	cancel()
	assert.Equal(t, "interrupted", readLine(t, r), "the process was asked (SIGINT), not killed")
	_ = tr.Wait()
	assert.Less(t, time.Since(start), grace, "a process that honours the interrupt is not waited out")
}

// TestSpawnChatTransport_KillsAfterGrace: a process that ignores the interrupt
// is killed once the grace has passed (WaitDelay) — and not before, because the
// interrupt is tried first. Its stdout ends, so a reader draining it returns.
func TestSpawnChatTransport_KillsAfterGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := `trap '' INT; echo ready; while :; do sleep 0.05; done`
	grace := 300 * time.Millisecond
	tr, err := spawnChatTransportGrace(ctx, "sh", []string{"-c", script}, nil, t.TempDir(), grace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Close() })
	r := bufio.NewReader(tr.stdout)
	require.Equal(t, "ready", readLine(t, r))

	start := time.Now()
	cancel()
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, r); close(drained) }()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("a process ignoring the interrupt was never killed")
	}
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, grace, "the kill waited out the grace")
	require.Error(t, tr.Wait(), "a killed process does not exit cleanly")
}

// TestSpawnChatTransport_WaitReportsACrash: a process that ends on its own
// with a failure reports it through Wait — the driver's evidence that a turn
// died mid-flight rather than finishing.
func TestSpawnChatTransport_WaitReportsACrash(t *testing.T) {
	tr, err := spawnChatTransportGrace(context.Background(), "sh", []string{"-c", "echo partial; exit 7"}, nil, t.TempDir(), time.Second)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, tr.stdout)
	var exitErr interface{ ExitCode() int }
	require.ErrorAs(t, tr.Wait(), &exitErr)
	assert.Equal(t, 7, exitErr.ExitCode())
}

// TestSpawnChatTransport_WaitKillsALingeringProcess: a process that closed its
// stdout but does not exit is killed once the grace has passed — its turn is
// over, and a reap that could hang would hold the turn open forever.
func TestSpawnChatTransport_WaitKillsALingeringProcess(t *testing.T) {
	tr, err := spawnChatTransportGrace(context.Background(), "sh", []string{"-c", "exec 1>&-; exec sleep 30"}, nil, t.TempDir(), 200*time.Millisecond)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, tr.stdout)
	done := make(chan error, 1)
	go func() { done <- tr.Wait() }()
	select {
	case err := <-done:
		require.Error(t, err, "a killed process does not exit cleanly")
	case <-time.After(10 * time.Second):
		t.Fatal("Wait hung on a process that closed stdout and lingered")
	}
}
