package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
	"github.com/ctxloom/ctxloom/internal/testsupport/lockstall"
)

// TestMain_AStalledLockWaitIsInLtkLogAfterTheProcessExits is ltk's durable
// leg: `ltk manage install` edits an engine settings file under the same
// home-rooted lock ctxloom's own settings writers take, and a wait on that
// lock past lockwait.After must leave a LogWaitExceeded record in
// ~/.ctxloom/logs/ltk.log that survives the process. A hook host rarely
// surfaces ltk's stderr, so without the log the stall leaves no trace.
//
// The child runs ltk's real main (TestMain's exitPinArgvEnv branch), so a main
// that stopped calling logboot.Install fails this. --print keeps the child off
// every other write, so the lock wait is the only thing it can log.
func TestMain_AStalledLockWaitIsInLtkLogAfterTheProcessExits(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(t.TempDir(), "settings.json")
	// The parent derives the child's paths from the same HOME the child runs
	// under; both are read live from the environment.
	t.Setenv("HOME", home)
	lockPath, err := paths.HomePathFor(settings)
	require.NoError(t, err)
	logPath, err := paths.HomeLogFilePath(progName)
	require.NoError(t, err)

	exe, err := os.Executable()
	require.NoError(t, err, "locating the test binary to re-exec")
	records := lockstall.Force(t, lockPath, logPath, func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(),
			exitPinArgvEnv+"=manage install --engine claude-code --settings "+settings+" --config= --print",
			"HOME="+home)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		require.NoError(t, cmd.Start())
		return cmd
	})

	require.Len(t, records, 1,
		"after the stalled ltk exited, %s should carry exactly one %q record", logPath, lockwait.LogWaitExceeded)
	assert.Equal(t, zapcore.WarnLevel.String(), records[0]["level"], "a wait past budget is a warning")
	assert.Equal(t, lockPath, records[0]["path"], "the record names the lock that was waited on")
}
