package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// startMain re-executes this test binary as taskloom (TestMain's
// exitPinArgvEnv branch runs the REAL main) with HOME and the working
// directory pinned, so the child resolves exactly the store the parent set up.
func startMain(t *testing.T, home, dir, argv string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err, "locating the test binary to re-exec")
	cmd := exec.Command(exe)
	cmd.Dir = dir
	// Later duplicates win in exec.Cmd.Env, so these override the sandbox's.
	cmd.Env = append(os.Environ(), exitPinArgvEnv+"="+argv, "HOME="+home)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	require.NoError(t, cmd.Start())
	return cmd
}

// taskLogUnder finds the one task log a seeded run left under root.
func taskLogUnder(t *testing.T, roots ...string) string {
	t.Helper()
	var found []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
				found = append(found, p)
			}
			return nil
		})
	}
	require.Len(t, found, 1, "seeding one task should leave exactly one task log")
	return found[0]
}

// TestMain_AStalledLockWaitIsInTaskloomLogAfterTheProcessExits is the durable
// leg for taskloom: a taskloom process whose lock wait on its task log outruns
// lockwait.After leaves a LogWaitExceeded record in ~/.ctxloom/logs/taskloom.log
// that is still there once the process is gone. Before taskloom installed its
// own logger that record went to zap's no-op global, and only a stderr line —
// which an MCP host or a script may never surface — said the store had stalled.
//
// The child runs taskloom's real main, so this proves the wiring, not just the
// logger: a main that stopped calling logboot.Install fails it. Two processes
// because "after the emitting process has exited" is the claim. The stall is
// FORCED — the parent holds the log's exclusive lock until the record has been
// written — so the test does not race the budget; the deadline only bounds a
// regression.
func TestMain_AStalledLockWaitIsInTaskloomLogAfterTheProcessExits(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	logPath := filepath.Join(home, ".ctxloom", "logs", "taskloom.log")

	seed := startMain(t, home, dir, "add stall-probe-seed")
	require.NoError(t, seed.Wait(), "seeding the store")
	lockPath := paths.PathFor(taskLogUnder(t, home, dir))

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

	child := startMain(t, home, dir, "list")
	deadline := time.Now().Add(lockwait.After + 30*time.Second)
	for time.Now().Before(deadline) && len(stallRecords(t, logPath)) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	unlock()

	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case err := <-waited:
		require.NoError(t, err, "the stalled taskloom did not exit cleanly once the lock was released")
	case <-time.After(30 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("the stalled taskloom never exited after the lock was released")
	}

	records := stallRecords(t, logPath)
	require.Len(t, records, 1,
		"after the stalled taskloom exited, %s should carry exactly one %q record", logPath, lockwait.LogWaitExceeded)
	assert.Equal(t, zapcore.WarnLevel.String(), records[0]["level"], "a wait past budget is a warning")
	assert.Equal(t, lockPath, records[0]["path"], "the record names the lock that was waited on")
}

// stallRecords parses the JSON log at path and returns its LogWaitExceeded
// records. A missing file is no records yet.
func stallRecords(t *testing.T, path string) []map[string]any {
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
