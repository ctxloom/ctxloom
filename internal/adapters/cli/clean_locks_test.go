package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The lock-sidecar step of `ctxloom clean`, asserted on the filesystem: which
// sidecars in ~/.ctxloom/locks exist after the command.

// stubCleanLockGate makes clean's gate answer from containers alone, over a
// listing that shows only this process.
func stubCleanLockGate(t *testing.T, containers ...string) {
	t.Helper()
	orig := cleanLockGate
	cleanLockGate = func() operations.LockReapGate {
		return operations.LockReapGate{
			Self: os.Getpid(),
			Processes: func() ([]operations.RunningProcess, error) {
				return []operations.RunningProcess{{PID: os.Getpid(), Comm: "cli.test"}}, nil
			},
			Version:    func(operations.RunningProcess) ([]byte, error) { return nil, nil },
			Containers: func(context.Context) ([]string, error) { return containers, nil },
		}
	}
	t.Cleanup(func() { cleanLockGate = orig })
}

// seedLockSidecars takes two home sidecars the way every taker does
// (safefs.LockGuarding on paths.HomePathFor), so each records what it
// guards, then removes one of the guarded files: one sidecar outlived its
// file, the other still guards one.
func seedLockSidecars(t *testing.T) (orphan, kept string) {
	t.Helper()
	work := t.TempDir()
	sidecar := func(target string) string {
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, []byte("{}"), 0o644))
		lp, err := paths.HomePathFor(target)
		require.NoError(t, err)
		lk, err := safefs.LockGuarding(safefs.New().Locks, lp, target)
		require.NoError(t, err)
		require.NoError(t, lk.Unlock())
		return lp
	}
	gone := filepath.Join(work, "gone", ".mcp.json")
	orphan = sidecar(gone)
	require.NoError(t, os.RemoveAll(filepath.Dir(gone)))
	kept = sidecar(filepath.Join(work, "kept", ".mcp.json"))
	return orphan, kept
}

// Without --yes clean reaps no sidecar, and reports the one it would.
func TestClean_LockSidecars_WithoutYesOnlyReports(t *testing.T) {
	cotProject(t)
	stubCleanLockGate(t)
	orphan, kept := seedLockSidecars(t)

	rep := cotRun(t, "--format", "json")

	assert.FileExists(t, orphan, "a report-only clean removed a sidecar")
	assert.FileExists(t, kept)
	assert.Equal(t, 1, rep.Locks.Orphaned)
}

// With --yes clean reaps the sidecar whose file is gone, and keeps the one
// whose file exists.
func TestClean_LockSidecars_YesReapsWhatOutlivedItsFile(t *testing.T) {
	cotProject(t)
	stubCleanLockGate(t)
	orphan, kept := seedLockSidecars(t)

	cotRun(t, "--yes", "--format", "json")

	assert.NoFileExists(t, orphan, "the sidecar whose file is gone survived --yes")
	assert.FileExists(t, kept, "a sidecar still guarding a file was reaped")
}

// A refusing gate keeps every sidecar under --yes, and the text report says
// why and what to do about it.
func TestClean_LockSidecars_ARefusalKeepsThemAndSaysWhy(t *testing.T) {
	cotProject(t)
	stubCleanLockGate(t, "ctxloom-iso-agent-7")
	orphan, _ := seedLockSidecars(t)

	out, err := execRootCmd(t, "clean", "--yes", "--format", "text")
	require.NoError(t, err)

	assert.FileExists(t, orphan, "a refused reap removed a sidecar")
	assert.Contains(t, out, "ctxloom-iso-agent-7")
	assert.Contains(t, out, "fix:")
}
