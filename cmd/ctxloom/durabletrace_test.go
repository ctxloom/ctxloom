package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
	"github.com/ctxloom/ctxloom/internal/shared/logboot"
)

// stalledLockEnv, present, tells a re-executed test binary to be the stalled
// process of TestInstall_AStalledLockWaitIsInTheLogFileAfterTheProcessExits;
// its value is the lock path to block on.
const stalledLockEnv = "CTXLOOM_DURABLETRACE_STALLED_LOCK"

// TestInstall_AStalledLockWaitIsInTheLogFileAfterTheProcessExits proves the
// durable leg end to end: a ctxloom process whose lock wait outruns
// lockwait.After leaves a LogWaitExceeded record in ~/.ctxloom/logs/ctxloom.log
// that is still there, readable, once that process is gone. The in-package
// tests prove the record is EMITTED (through an observer); only this one proves
// the process logger main installs actually carries it to the file — the
// channel an operator reads after an unwatched stall.
//
// Two processes because "after the emitting process has exited" is the claim:
// the child installs the logger main installs (logboot.Install), stalls on a real flock
// the parent holds, and exits; the parent reads the file only after that.
func TestInstall_AStalledLockWaitIsInTheLogFileAfterTheProcessExits(t *testing.T) {
	if lockPath := os.Getenv(stalledLockEnv); lockPath != "" {
		flush := logboot.Install("ctxloom", false)
		code := 0
		if err := filelock.WithLock(afero.NewOsFs(), lockPath, func() error { return nil }); err != nil {
			code = 2
		}
		flush()
		os.Exit(code)
	}

	logPath := logHome(t)
	lockPath := filepath.Join(t.TempDir(), "held.lock")

	held, release, lockDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		lockDone <- filelock.WithLock(afero.NewOsFs(), lockPath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer releaseOnce()

	child := exec.Command(os.Args[0], "-test.run=^TestInstall_AStalledLockWaitIsInTheLogFileAfterTheProcessExits$", "-test.timeout=120s")
	child.Env = append(os.Environ(), stalledLockEnv+"="+lockPath)
	require.NoError(t, child.Start())

	// Hold the lock until the stall has been reported, so the child is
	// genuinely parked past its budget; the deadline only bounds a regression.
	deadline := time.Now().Add(lockwait.After + 30*time.Second)
	for time.Now().Before(deadline) && len(waitRecords(t, logPath)) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	releaseOnce()
	require.NoError(t, <-lockDone)

	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case err := <-waited:
		require.NoError(t, err, "the stalled child did not exit cleanly once the lock was released")
	case <-time.After(30 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("the child never exited after the lock was released")
	}

	records := waitRecords(t, logPath)
	require.Len(t, records, 1,
		"after the stalled process exited, %s should carry exactly one %q record", logPath, lockwait.LogWaitExceeded)
	assert.Equal(t, zapcore.WarnLevel.String(), records[0]["level"], "a wait past budget is a warning")
	assert.Equal(t, lockPath, records[0]["path"], "the record names the lock that was waited on")
}

// waitRecords parses the JSON log at path and returns its LogWaitExceeded
// records. A missing file is no records yet.
func waitRecords(t *testing.T, path string) []map[string]any {
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
