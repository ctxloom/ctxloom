//go:build !windows

package testenv

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/testsupport/procalive"
)

// testBound is far below any real command's run time, so every command below
// outlives it; testGuard is this test's OWN ceiling on a call that must not
// hang. The gap between them is the claim: a command past its bound fails
// within about the bound, not at the command's natural end (sleepSeconds).
const (
	testBound    = 200 * time.Millisecond
	testGuard    = 3 * time.Second
	sleepSeconds = "30"
)

// callWithin runs call and fails the test if it has not returned within
// testGuard — so a runner with no deadline goes RED here instead of hanging
// the package until go test's own timeout.
func callWithin(t *testing.T, what string, call func() error) (time.Duration, error) {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- call() }()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(testGuard):
		t.Fatalf("%s did not return within %s: a command that outlives its %s bound must be killed, not waited on", what, testGuard, testBound)
		return 0, nil
	}
}

func TestExec_ACommandPastItsBoundFailsNamingTheCommandAndTheBound(t *testing.T) {
	var h RunHistory
	h.SetCommandBound(testBound)
	cmd := exec.Command("sleep", sleepSeconds)

	elapsed, err := callWithin(t, "Exec", func() error { return h.Exec(cmd) })

	var de *DeadlineError
	if !errors.As(err, &de) {
		t.Fatalf("Exec error = %v (%T), want a *DeadlineError", err, err)
	}
	if elapsed < testBound {
		t.Errorf("Exec returned after %s, before its %s bound", elapsed, testBound)
	}
	msg := de.Error()
	for _, want := range []string{"sleep " + sleepSeconds, testBound.String()} {
		if !strings.Contains(msg, want) {
			t.Errorf("deadline error %q does not name %q", msg, want)
		}
	}
	// A step that discards the error still sees why its command died.
	if !strings.Contains(h.LastOutput(), msg) {
		t.Errorf("recorded output %q does not carry the deadline error", h.LastOutput())
	}
	if h.LastExitCode() == 0 {
		t.Error("a killed command was recorded as exit 0")
	}
	// The expiry is handed out once, so the scenario hook fails the step that
	// ran the command and not every step after it.
	if got := h.TakeExpired(); !errors.Is(got, err) {
		t.Errorf("TakeExpired() = %v, want the deadline error %v", got, err)
	}
	if got := h.TakeExpired(); got != nil {
		t.Errorf("second TakeExpired() = %v, want nil", got)
	}
}

// The GROUP is killed, not just the direct child. A grandchild inherits the
// output pipes, and Wait cannot return while any writer holds them — so
// killing only the child leaves the runner blocked on the grandchild, which
// is the hang the bound exists to end.
func TestExec_TheBoundKillsTheWholeProcessGroup(t *testing.T) {
	var h RunHistory
	h.SetCommandBound(testBound)
	// The shell prints its background child's pid, then waits on it.
	cmd := exec.Command("sh", "-c", "sleep "+sleepSeconds+" & echo $!; wait")

	elapsed, err := callWithin(t, "Exec", func() error { return h.Exec(cmd) })

	var de *DeadlineError
	if !errors.As(err, &de) {
		t.Fatalf("Exec error = %v (%T), want a *DeadlineError", err, err)
	}
	if limit := testBound + time.Second; elapsed > limit {
		t.Errorf("Exec took %s: the grandchild held the pipes past the kill, so the group was not killed", elapsed)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(strings.SplitN(h.LastStdout(), "\n", 2)[0]))
	if perr != nil {
		t.Fatalf("no grandchild pid in stdout %q: %v", h.LastStdout(), perr)
	}
	deadline := time.Now().Add(testGuard)
	for procalive.Alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d outlived the group kill", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// RunWithStdin is the other path a scenario's command takes. Its bound runs
// from the moment stdin is closed, after the harness's own stdin hold.
func TestRunWithStdin_ACommandPastItsBoundFails(t *testing.T) {
	e := &TestEnvironment{AppBinary: "/bin/sh", ProjectDir: t.TempDir(), HomeDir: t.TempDir()}
	e.SetCommandBound(testBound)

	_, err := callWithin(t, "RunWithStdin", func() error {
		return e.RunWithStdin("not json-rpc\n", "-c", "cat >/dev/null; sleep "+sleepSeconds)
	})

	var de *DeadlineError
	if !errors.As(err, &de) {
		t.Fatalf("RunWithStdin error = %v (%T), want a *DeadlineError", err, err)
	}
	if got := e.TakeExpired(); !errors.Is(got, err) {
		t.Errorf("TakeExpired() = %v, want the deadline error %v", got, err)
	}
}

// A command that finishes inside its bound is untouched: its own exit status
// comes back, and nothing is left for the scenario hook to report.
func TestExec_ACommandInsideItsBoundKeepsItsOwnResult(t *testing.T) {
	var h RunHistory
	h.SetCommandBound(testGuard)
	err := h.Exec(exec.Command("sh", "-c", "echo hi; exit 3"))

	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("Exec error = %v, want exit status 3", err)
	}
	if h.LastOutput() != "hi\n" {
		t.Errorf("LastOutput() = %q, want %q", h.LastOutput(), "hi\n")
	}
	if got := h.TakeExpired(); got != nil {
		t.Errorf("TakeExpired() = %v, want nil", got)
	}
}
