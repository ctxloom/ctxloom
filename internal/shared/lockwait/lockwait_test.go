package lockwait_test

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// waitHelperEnv, present, tells a re-executed test binary to block instead of
// running the parent side of TestWatch_ReportsAWaitItCannotEnd.
const waitHelperEnv = "CTXLOOM_LOCKWAIT_WAIT_HELPER"

// waitNotice is the substring an operator has to see. The label is part of
// it: "something is stuck" is not actionable, "stuck on THIS thing" names
// what to look for.
const waitNotice = "still waiting for lock on "

// TestWatch_ReportsAWaitItCannotEnd carries forward filelock's
// waitnotice_test.go contract (the package this replaced): a caller blocked
// on something that never completes must say so, on real stderr, in a place
// an operator is looking, while it waits.
//
// Two processes, not two goroutines: an in-process test could observe a
// notice written by anything, whereas a child that inherits nothing but a
// "block forever" instruction proves the notice comes out of a genuinely
// blocked call, on real stderr.
func TestWatch_ReportsAWaitItCannotEnd(t *testing.T) {
	if os.Getenv(waitHelperEnv) != "" {
		// Child: watch a call that never returns. It is killed from there;
		// reaching past the block would be a bug in the fixture, not this
		// package.
		stop := lockwait.Watch("the child's forever-blocked call")
		defer stop()
		select {}
	}

	child := exec.Command(os.Args[0], "-test.run=TestWatch_ReportsAWaitItCannotEnd", "-test.timeout=120s")
	child.Env = append(os.Environ(), waitHelperEnv+"=1")
	stderr, err := child.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, child.Start())
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		scan := bufio.NewScanner(stderr)
		for scan.Scan() {
			lines <- scan.Text()
		}
	}()

	want := waitNotice + "the child's forever-blocked call"
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the blocked child stopped writing without ever reporting the wait; wanted a line containing %q", want)
			}
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("a process blocked for 30s on a watched call never said so: no line containing %q on its stderr", want)
		}
	}
}

// TestWatch_StopReturnsPromptlyWhenTheOperationFinishesFirst is the
// counterpart: a call that completes well before After must have its stop()
// return immediately, not linger until the watchdog's timer would have
// fired — proving stop() actually stands the watchdog down rather than just
// disarming its printing.
func TestWatch_StopReturnsPromptlyWhenTheOperationFinishesFirst(t *testing.T) {
	start := time.Now()
	stop := lockwait.Watch("a fast call")
	stop()
	require.Less(t, time.Since(start), lockwait.After,
		"stop() must return well before the watchdog's own timer, or it is not actually standing the goroutine down")
}
