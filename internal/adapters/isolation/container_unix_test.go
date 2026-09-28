//go:build !windows

// Unix directory permissions: a write-protected or 0000 directory blocks removal or stat only there.

package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brokenScratch builds a scratch tree RemoveAll cannot fully remove (a file
// pinned inside a write-protected subdir) — the hermetic stand-in for the
// root-owned residue a wrong-identity container leaves behind. Perms are
// restored on cleanup so t.TempDir's own removal succeeds.
func brokenScratch(t *testing.T) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root ignores directory write protection; cannot simulate immovable residue")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "cfg0")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "stuck"), []byte("x"), 0o644))
	require.NoError(t, os.Chmod(sub, 0o555))
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	return root
}

// TestContainerWorkspace_CleanupSurfacesResidue: a scratch tree the launching
// user cannot remove is the CONSEQUENCE DETECTOR for every identity hole (a
// wrong-identity container root-owned it) — the failure must stream loudly,
// naming the residue path, the likely cause, and a manual fix, never be
// silently swallowed (the callers discard Cleanup's error by contract).
func TestContainerWorkspace_CleanupSurfacesResidue(t *testing.T) {
	root := brokenScratch(t)
	ws := &containerWorkspace{dir: "/proj", scratchRoot: root, agentID: "m"}

	done := captureStderr(t)
	err := ws.Cleanup()
	stderr := done()

	require.Error(t, err, "the error still returns for callers that check")
	assert.Contains(t, err.Error(), "remove container scratch")
	assert.Contains(t, stderr, root, "the warning names the residue path")
	assert.Contains(t, stderr, "wrong-identity", "…and the likely cause")
	assert.Contains(t, stderr, "sudo rm", "…and the manual fix")
}

// TestContainerWorkspace_WorktreeBaseCleanupSurfacesResidue: the worktree-base
// workspace (baseCleanup = the worktree teardown) surfaces the same scratch
// residue the host base does — post-collapse both bases share one containerWorkspace
// whose Cleanup always warns AND returns the scratch error (SD3), the base teardown
// (WIP-safe) contributing no error of its own.
func TestContainerWorkspace_WorktreeBaseCleanupSurfacesResidue(t *testing.T) {
	root := brokenScratch(t)
	ws := &containerWorkspace{scratchRoot: root, agentID: "m", baseCleanup: (&worktreeWorkspace{}).Cleanup}

	done := captureStderr(t)
	err := ws.Cleanup()
	stderr := done()

	require.Error(t, err, "the scratch-removal error returns for callers that check")
	assert.Contains(t, err.Error(), "remove container scratch")
	assert.Contains(t, stderr, root, "the warning names the residue path")
	assert.Contains(t, stderr, "sudo rm", "…and the manual fix")
}

// TestContainerWorkspace_CleanupSurfacesBaseError pins a regression. The
// base teardown's error was discarded with `_ =` under a comment asserting it
// "never contributes an error" — true today only because worktreeWorkspace.
// Cleanup happens to return nil unconditionally (it warns instead), which is a
// property of a DIFFERENT type in a different file that nothing binds to this
// one. The moment a base teardown does report a failure it would vanish. Join
// it instead, so the guarantee is structural rather than remote.
func TestContainerWorkspace_CleanupSurfacesBaseError(t *testing.T) {
	baseErr := fmt.Errorf("worktree teardown failed")

	// Base failure alone: nothing else went wrong, and it still surfaces.
	ws := &containerWorkspace{dir: "/proj", agentID: "m", baseCleanup: func() error { return baseErr }}
	err := ws.Cleanup()
	require.Error(t, err, "a base teardown failure must not be swallowed")
	assert.ErrorIs(t, err, baseErr)

	// Both halves fail: neither hides the other.
	root := brokenScratch(t)
	both := &containerWorkspace{dir: "/proj", agentID: "m", scratchRoot: root, baseCleanup: func() error { return baseErr }}
	done := captureStderr(t)
	err = both.Cleanup()
	_ = done()
	require.Error(t, err)
	assert.ErrorIs(t, err, baseErr)
	assert.Contains(t, err.Error(), "remove container scratch")
}

// TestContainer_GitdirMirrorMountUnreadableGit pins a regression. The
// guard was `if err != nil || info.IsDir()` — one branch for two opposite facts.
// "no .git" and "a .git directory" genuinely need no mirror, but an UNREADABLE
// .git means we could not tell which case we are in, and answering "no mirror
// needed" hands the container a checkout whose git cannot resolve the repo. The
// container axis's whole degrade contract is fatal-unless-degraded on a lost
// boundary, so this must error out of PrepareWorkspace, never resolve silently.
func TestContainer_GitdirMirrorMountUnreadableGit(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions; cannot make .git unstattable")
	}
	proj := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".git"),
		[]byte("gitdir: /repo/.git/worktrees/x\n"), 0o644))
	require.NoError(t, os.Chmod(proj, 0o000))
	t.Cleanup(func() { _ = os.Chmod(proj, 0o755) })

	_, ok, err := gitdirMirrorMount(context.Background(),
		fakeRuntime{name: "docker", available: true}, &git.Fake{CommonDirValue: "/repo/.git"}, proj)
	require.Error(t, err, "an unreadable .git must fail the workspace, not silently yield no mirror")
	assert.False(t, ok)
	assert.Contains(t, err.Error(), ".git")
}
