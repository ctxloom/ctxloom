//go:build !windows

// A 0000 parent directory denies stat only under Unix permissions; Windows mode bits deny nothing.

package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorktreeRemoved_UnreadableIsNotGone is a red-first pin. The
// REAPED-vs-SPARED verdict was `if _, statErr := os.Stat(wtDir); statErr != nil`,
// which reads EVERY stat failure as "the tree is gone": EACCES on a parent
// directory, ELOOP, ENAMETOOLONG. The sweep then reports a worktree it never
// removed as reaped, and the summary the boot transcript prints is wrong in the
// one direction that matters — claiming cleanup that did not happen. Only
// ErrNotExist proves removal.
func TestWorktreeRemoved_UnreadableIsNotGone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not deny stat")
	}
	parent := filepath.Join(t.TempDir(), "sealed")
	wtDir := filepath.Join(parent, "ctxloom-wt-x")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	_, statErr := os.Stat(wtDir)
	require.Error(t, statErr, "the fixture must actually make stat fail")
	require.False(t, os.IsNotExist(statErr), "…and fail for a reason other than absence")

	assert.False(t, worktreeRemoved(wtDir),
		"an unreadable path is not proof of removal — the tree may well still be there")

	require.NoError(t, os.Chmod(parent, 0o755))
	assert.False(t, worktreeRemoved(wtDir), "a readable, still-present tree was not removed")
	require.NoError(t, os.RemoveAll(wtDir))
	assert.True(t, worktreeRemoved(wtDir), "an absent tree WAS removed")
}
