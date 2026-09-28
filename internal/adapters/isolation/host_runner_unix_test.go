//go:build !windows

package isolation

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestHelperStoppableRunner is not a real test — it is the runner the
// HostRunner.Kill tests start through startHostRunner. It reports readiness,
// then either unwinds on SIGTERM and records that its teardown ran
// ("graceful"), or ignores SIGTERM and hangs ("wedged").
func TestHelperStoppableRunner(t *testing.T) {
	mode, dir, ok := strings.Cut(os.Getenv("CTXLOOM_STOPPABLE_RUNNER"), ":")
	if !ok {
		return
	}
	var ctx context.Context = context.Background()
	if mode == "wedged" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, syscall.SIGTERM)
		defer stop()
	}
	if err := os.WriteFile(dir+"/ready", nil, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write ready:", err)
		os.Exit(1)
	}
	select {
	case <-ctx.Done():
		_ = os.WriteFile(dir+"/torn-down", nil, 0o600)
		os.Exit(0)
	case <-time.After(100 * time.Second):
		os.Exit(1)
	}
}

func startStoppableRunner(t *testing.T, mode string, grace time.Duration) (*HostRunner, string) {
	t.Helper()
	dir := t.TempDir()
	h, err := startHostRunnerWithGrace([]string{"-test.run=^TestHelperStoppableRunner$"},
		map[string]string{"CTXLOOM_STOPPABLE_RUNNER": mode + ":" + dir}, grace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(h.pid, syscall.SIGKILL) })
	require.Eventually(t, func() bool { _, err := os.Stat(dir + "/ready"); return err == nil },
		5*time.Second, 10*time.Millisecond, "the runner never became ready; its stderr: %s", stderrOf{h})
	return h, dir
}

// An orderly stop must let the runner run its teardown (PaneHost.Stop, temp
// cleanup): Kill asks with SIGTERM first. The grace is far longer than the
// bound on Kill, so passing also proves Kill did not sit out the grace.
func TestHostRunnerKill_RunsTheRunnersTeardown(t *testing.T) {
	h, dir := startStoppableRunner(t, "graceful", time.Minute)

	testsupport.Within(t, 10*time.Second, func() struct{} { h.Kill(); return struct{}{} },
		"Kill must return once a runner that honours SIGTERM has exited")
	require.FileExists(t, dir+"/torn-down", "the runner must have unwound through its teardown, not been SIGKILLed")
	require.False(t, processAlive(h.pid))
}

// A runner that ignores SIGTERM must not wedge the stop: after the grace it
// is SIGKILLed, and Kill returns with the runner gone.
func TestHostRunnerKill_SIGKILLsAWedgedRunnerAfterTheGrace(t *testing.T) {
	h, dir := startStoppableRunner(t, "wedged", 300*time.Millisecond)

	testsupport.Within(t, 10*time.Second, func() struct{} { h.Kill(); return struct{}{} },
		"Kill must not wait on a wedged runner past the grace")
	require.False(t, processAlive(h.pid), "a wedged runner must be SIGKILLed once the grace runs out")
	require.NoFileExists(t, dir+"/torn-down")
}

// stderrOf renders a runner's stderr tail when a failure message is built.
type stderrOf struct{ h *HostRunner }

func (s stderrOf) String() string { return s.h.StderrTail() }
