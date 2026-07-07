package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRuntime is a ContainerRuntime stub for the degrade-path tests: it reports a
// configurable availability and binary without touching a real daemon.
type fakeRuntime struct {
	name      string
	binary    string
	available bool
}

func (f fakeRuntime) Name() string             { return f.name }
func (f fakeRuntime) Binary() string           { return f.binary }
func (f fakeRuntime) Available() bool          { return f.available }
func (fakeRuntime) RunArgs(RunSpec) []string   { return nil }
func (fakeRuntime) RemoveArgs(string) []string { return nil }

// TestContainer_Axes pins the policy's identity: name "container", approvals
// BYPASS (the container is the boundary that replaces the in-engine prompt).
func TestContainer_Axes(t *testing.T) {
	c := NewContainer(fakeRuntime{name: "docker", available: true}, "img")
	assert.Equal(t, "container", c.Name())
	assert.Equal(t, ApprovalsBypass, c.Approvals(), "isolated runs bypass the in-engine approval prompt")
}

// TestContainer_PrepareDegrades: an unavailable runtime OR a missing image makes
// PrepareWorkspace return an error so the caller falls back to None — never blocks.
func TestContainer_PrepareDegrades(t *testing.T) {
	ctx := context.Background()

	// Runtime cannot launch → error mentioning the runtime.
	_, err := NewContainer(fakeRuntime{name: "docker", available: false}, "img").
		PrepareWorkspace(ctx, "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot launch")

	// Runtime available but image absent (binary "" → imagePresent false) → error.
	_, err = NewContainer(fakeRuntime{name: "docker", binary: "", available: true}, "img").
		PrepareWorkspace(ctx, "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not present")
}

// TestContainerWorkspace_DirAndCleanup: Dir() is the identical-path project dir;
// Cleanup removes the host socket scratch and is idempotent.
func TestContainerWorkspace_DirAndCleanup(t *testing.T) {
	scratch, err := os.MkdirTemp("", "ctxloom-iso-test-")
	require.NoError(t, err)
	ws := &containerWorkspace{dir: "/proj", scratchRoot: scratch, agentID: "m"}

	assert.Equal(t, "/proj", ws.Dir(), "workspace dir is the identical-path project directory")
	require.NoError(t, ws.Cleanup())
	_, statErr := os.Stat(scratch)
	assert.True(t, os.IsNotExist(statErr), "cleanup removes the scratch tree")
	assert.NoError(t, ws.Cleanup(), "cleanup is idempotent")
}

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

// TestContainerWorktreeWorkspace_CleanupSurfacesResidue: the composed
// workspace's scratch removal was a bare `_ = os.RemoveAll` — same residue,
// same loud surfacing (its Cleanup contract stays never-error: the worktree
// half's WIP-safety owns that semantic).
func TestContainerWorktreeWorkspace_CleanupSurfacesResidue(t *testing.T) {
	root := brokenScratch(t)
	ws := &containerWorktreeWorkspace{wt: &worktreeWorkspace{}, scratchRoot: root, agentID: "m"}

	done := captureStderr(t)
	err := ws.Cleanup()
	stderr := done()

	assert.NoError(t, err, "the composed cleanup never errors (WIP-safe contract)")
	assert.Contains(t, stderr, root, "the warning names the residue path")
	assert.Contains(t, stderr, "sudo rm", "…and the manual fix")
}

// TestContainer_GitdirMirrorMount is finding-2's unit: when the LIVE project is
// itself a linked worktree (or submodule) its .git is a POINTER FILE whose common
// dir lives OUTSIDE the identical-path project mount, so the plain container must
// mirror that common dir (same fix ContainerWorktree uses) — but a normal .git
// DIRECTORY (main-repo checkout) is already covered by the project mount and needs
// no extra mount, and a non-repo project needs none either.
func TestContainer_GitdirMirrorMount(t *testing.T) {
	ctx := context.Background()
	const common = "/repo/.git"

	c := NewContainer(fakeRuntime{name: "docker", available: true}, "img")
	c.git = &git.Fake{CommonDirValue: common}

	// .git is a POINTER FILE → mirror the common dir identical-path.
	fileProj := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fileProj, ".git"),
		[]byte("gitdir: /repo/.git/worktrees/x\n"), 0o644))
	m, ok, err := c.gitdirMirrorMount(ctx, fileProj)
	require.NoError(t, err)
	require.True(t, ok, "a .git POINTER FILE (linked worktree/submodule) needs the common-dir mirror")
	assert.Equal(t, Mount{Host: common, Container: common}, m,
		"the common dir is mirrored identical-path so gitdir resolves in-container")

	// .git is a DIRECTORY → already inside the identical-path project mount.
	dirProj := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dirProj, ".git"), 0o755))
	_, ok, err = c.gitdirMirrorMount(ctx, dirProj)
	require.NoError(t, err)
	assert.False(t, ok, "a normal .git directory is covered by the project mount; no mirror")

	// No repo at all → nothing to mirror.
	bareProj := t.TempDir()
	_, ok, err = c.gitdirMirrorMount(ctx, bareProj)
	require.NoError(t, err)
	assert.False(t, ok, "a non-repo project needs no gitdir mirror")
}

// TestContainerName_SanitizesAndScopes: the name is a valid, unique,
// teardown-targetable container name derived from the agent id.
func TestContainerName_SanitizesAndScopes(t *testing.T) {
	n := containerName("code review/aspect:sec")
	assert.True(t, strings.HasPrefix(n, "ctxloom-iso-"), "scoped name prefix")
	assert.NotContains(t, n, "/", "path separators stripped")
	assert.NotContains(t, n, ":", "colons stripped")
	assert.NotEqual(t, containerName("m"), containerName("m"), "names are unique per spawn")

	// An empty/garbage agent id still yields a valid name.
	assert.True(t, strings.HasPrefix(containerName("///"), "ctxloom-iso-agent-"))
}

// TestResolveContainer_DegradesWithoutRuntime documents the two-place degrade: with
// no runtime Resolve returns None; with a runtime it returns a container policy.
func TestResolveContainer_DegradesWithoutRuntime(t *testing.T) {
	p := Resolve(Axes{Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
	if (Docker{}).Available() || (Podman{}).Available() {
		assert.Equal(t, "container", p.Name(), "a launchable runtime resolves to the container policy")
	} else {
		assert.Equal(t, "none", p.Name(), "no runtime degrades to none")
	}
}
