package filelock_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// waitHelperEnv carries the lock path to the child half of the test below.
// Its presence is what tells a re-executed test binary to block on a lock
// instead of running the parent side again.
const waitHelperEnv = "CTXLOOM_FILELOCK_WAIT_HELPER"

// waitNotice is the substring an operator has to see. The path is part of it:
// "something is stuck" is not actionable, "stuck on THIS file" names what to
// look for and what holds it.
const waitNotice = "still waiting for lock on "

// Acquisition here is unconditionally blocking, so a holder that never
// releases parks the caller forever — `taskloom status` reaches this on its
// exclusive write path and simply never returns. That the wait is unbounded
// is a policy question this package cannot settle alone; that the wait is
// INVISIBLE is not, and it is the whole difference between a support ticket
// and an operator who knows what to kill. A run blocked on a lock another
// process holds must say so, in a place a human is looking, while it waits.
//
// Two processes, not two goroutines: an in-process test could observe a
// notice written by anything, whereas a child that inherits nothing but the
// path proves the notice comes out of the blocked acquisition itself, on real
// stderr.
func TestLock_ReportsAWaitItCannotEnd(t *testing.T) {
	if path := os.Getenv(waitHelperEnv); path != "" {
		// Child: block forever on the lock the parent holds. It is killed
		// from there; reaching the release would mean the parent let go.
		unlock, err := filelock.Lock(path)
		defer unlock()
		require.NoError(t, err)
		return
	}

	path := filepath.Join(t.TempDir(), "guard.lock")
	unlock, err := filelock.Lock(path)
	require.NoError(t, err)
	defer unlock()

	child := exec.Command(os.Args[0], "-test.run=TestLock_ReportsAWaitItCannotEnd", "-test.timeout=120s")
	child.Env = append(os.Environ(), waitHelperEnv+"="+path)
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

	want := waitNotice + path
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
			t.Fatalf("a process blocked for 30s on a lock held by another process never said so: no line containing %q on its stderr", want)
		}
	}
}
