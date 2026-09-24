// Package lockstall proves, across a process boundary, that a binary's stalled
// lock wait leaves its lockwait.LogWaitExceeded record in that binary's own
// log. "After the emitting process has exited" is the claim, so the waiter is
// a separate process; the stall is FORCED by holding the lock, never raced
// against lockwait.After.
package lockstall

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// exitBound caps how long a released waiter may take to finish. It bounds a
// regression only; nothing waits on it in a passing run.
const exitBound = 30 * time.Second

// Force holds the exclusive lock at lockPath, starts the waiter with start,
// and keeps holding until logPath carries a LogWaitExceeded record (or a
// deadline that only bounds a regression). It then releases the lock,
// requires the waiter to exit cleanly, and returns the log's LogWaitExceeded
// records as they stand AFTER the exit.
//
// start must return a command that is already running and that will block
// acquiring lockPath.
func Force(t testing.TB, lockPath, logPath string, start func() *exec.Cmd) []map[string]any {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o700))
	held := flock.New(lockPath)
	require.NoError(t, held.Lock())
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			require.NoError(t, held.Unlock())
		}
	}
	defer unlock()

	child := start()
	deadline := time.Now().Add(lockwait.After + exitBound)
	for time.Now().Before(deadline) && len(Records(t, logPath)) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	unlock()

	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case err := <-waited:
		require.NoError(t, err, "the stalled process did not exit cleanly once the lock was released")
	case <-time.After(exitBound):
		_ = child.Process.Kill()
		t.Fatal("the stalled process never exited after the lock was released")
	}
	return Records(t, logPath)
}

// Records parses the JSON log at path and returns its LogWaitExceeded
// records. A missing file is no records yet.
func Records(t testing.TB, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []map[string]any
	scan := bufio.NewScanner(bytes.NewReader(data))
	for scan.Scan() {
		var rec map[string]any
		if json.Unmarshal(scan.Bytes(), &rec) != nil {
			continue // a partially written line mid-poll; the post-exit read is whole
		}
		if rec["msg"] == lockwait.LogWaitExceeded {
			out = append(out, rec)
		}
	}
	return out
}
