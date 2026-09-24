package lockwait_test

import (
	"bufio"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// waitHelperEnv, present, tells a re-executed test binary to block instead of
// running the parent side of TestWatch_ReportsAWaitItCannotEnd.
const waitHelperEnv = "CTXLOOM_LOCKWAIT_WAIT_HELPER"

// TestWatch_ReportsAWaitItCannotEnd: a caller blocked on something that
// never completes must say so, on real stderr, in a place an operator is
// looking, while it waits. The line is compared whole, not by substring, so a
// notice that drops or mangles the label fails.
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

	want := lockwait.WaitNotice("the child's forever-blocked call")
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the blocked child stopped writing without ever reporting the wait; wanted the line %q", want)
			}
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("a process blocked for 30s on a watched call never said so: no line %q on its stderr", want)
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

// TestWatch_AWaitPastItsBudgetLeavesAStructuredRecord is the durable half of
// the contract. The stderr line above reaches an operator who is watching;
// on a runner whose stderr is an unread pty it reaches nobody, and after the
// process exits there is nothing left to read. The structured log is the
// channel that outlives the process, so a wait past After must also land
// there — as a record with a level, a message and the label as a field, not
// as text to be scraped.
//
// The observer replaces the process-wide logger for the duration, so nothing
// here depends on a log file or on stderr at all.
func TestWatch_AWaitPastItsBudgetLeavesAStructuredRecord(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	stop := lockwait.Watch("a call parked past its budget")
	defer stop()

	require.Eventually(t, func() bool { return logs.Len() > 0 },
		lockwait.After+10*time.Second, 50*time.Millisecond,
		"a wait past After left no structured record; only the stderr line exists, and that dies with the process")

	entries := logs.FilterMessage(lockwait.LogWaitExceeded).All()
	require.Len(t, entries, 1, "exactly one record per watched wait; got %v", logs.All())
	assert.Equal(t, zapcore.WarnLevel, entries[0].Level, "a wait past budget is a warning, not information")
	assert.Equal(t, "a call parked past its budget", entries[0].ContextMap()["path"],
		"the record names WHAT was waited on, or it cannot be matched to a stall afterwards")
}
