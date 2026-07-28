package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRuntime is a Runtime stub for the degrade-path tests: it reports a
// configurable availability and binary without touching a real daemon.
type fakeRuntime struct {
	name      string
	binary    string
	available bool
}

func (f fakeRuntime) Name() string                                     { return f.name }
func (f fakeRuntime) Binary() string                                   { return f.binary }
func (f fakeRuntime) Available() bool                                  { return f.available }
func (fakeRuntime) RunArgs(RunSpec) []string                           { return nil }
func (fakeRuntime) RemoveArgs(string) []string                         { return nil }
func (fakeRuntime) ExecArgs(string, bool, []string, []string) []string { return nil }

// Spawn is never reached by the Prepare-only degrade tests (they stop at the gate
// or the mount wiring); it errors loudly if one ever does call it.
func (fakeRuntime) Spawn(LaunchSpec) (pb.Client, error) {
	return nil, fmt.Errorf("fakeRuntime: Spawn not expected in these tests")
}

// Expose is the OCI identity bind mount, so tests that route delivery mounts
// through the runtime (sessionStateMounts, gitCommonDirMount) see the same Mount
// the literal produced.
func (fakeRuntime) Expose(host, target string, readOnly bool) Mount {
	return Mount{Host: host, Container: target, ReadOnly: readOnly}
}

// ExposeIdentical mirrors ociRuntime's default (identityMapper): fakeRuntime
// carries no pathMapper, so this is Expose(hostPath, hostPath, readOnly).
func (fakeRuntime) ExposeIdentical(hostPath string, readOnly bool) Mount {
	return Mount{Host: hostPath, Container: hostPath, ReadOnly: readOnly}
}

// mapper is always identity — fakeRuntime never carries a lanky-pod mapper;
// tests that need to exercise a non-identity mapper inject one directly at
// buildRunSpec (see runner_test.go), not through this fake.
func (fakeRuntime) mapper() pathMapper { return identityMapper{} }

// TestContainer_MCPCommandOverride pins dire-five's fix at its source: a
// container policy (either base tier — hostBase and worktreeBase share the
// same binaryPath field) reports the in-container ctxloom binary
// (defaultContainerBinary) as its MCP command override, the single source of
// truth threaded from NewContainerFor. This is the value
// operations.MCPCommandOverrideForPolicy relays onto the run env
// (agent.MCPCommandOverrideEnv) so the MCP-surface writer stamps a `command`
// the container can actually exec instead of the host self-exec path.
func TestContainer_MCPCommandOverride(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "")
	assert.Equal(t, defaultContainerBinary, c.MCPCommandOverride())
	assert.Equal(t, "/usr/local/bin/ctxloom", c.MCPCommandOverride(), "the documented in-container path — a change here is a wire-contract change")

	wt := NewContainerWorktreeFor(fakeRuntime{name: "docker", available: true}, "", ImageConfig{}, nil)
	assert.Equal(t, defaultContainerBinary, wt.MCPCommandOverride(), "the worktree-in-container base shares the same binaryPath field")
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
	// Safety net: this test's whole point is exercising ws.Cleanup() itself, so
	// a mutant that breaks its removal logic (or an earlier failure/panic)
	// must not leak this fixture dir under the OS temp dir.
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
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

// TestContainer_GitdirMirrorMount is finding-2's unit: when the LIVE project is
// itself a linked worktree (or submodule) its .git is a POINTER FILE whose common
// dir lives OUTSIDE the identical-path project mount, so the plain container must
// mirror that common dir (same fix the worktree base uses) — but a normal .git
// DIRECTORY (main-repo checkout) is already covered by the project mount and needs
// no extra mount, and a non-repo project needs none either.
func TestContainer_GitdirMirrorMount(t *testing.T) {
	ctx := context.Background()
	const common = "/repo/.git"

	rt := fakeRuntime{name: "docker", available: true}
	g := &git.Fake{CommonDirValue: common}

	// .git is a POINTER FILE → mirror the common dir identical-path.
	fileProj := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fileProj, ".git"),
		[]byte("gitdir: /repo/.git/worktrees/x\n"), 0o644))
	m, ok, err := gitdirMirrorMount(ctx, rt, g, fileProj)
	require.NoError(t, err)
	require.True(t, ok, "a .git POINTER FILE (linked worktree/submodule) needs the common-dir mirror")
	assert.Equal(t, Mount{Host: common, Container: common}, m,
		"the common dir is mirrored identical-path so gitdir resolves in-container")

	// .git is a DIRECTORY → already inside the identical-path project mount.
	dirProj := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dirProj, ".git"), 0o755))
	_, ok, err = gitdirMirrorMount(ctx, rt, g, dirProj)
	require.NoError(t, err)
	assert.False(t, ok, "a normal .git directory is covered by the project mount; no mirror")

	// No repo at all → nothing to mirror.
	bareProj := t.TempDir()
	_, ok, err = gitdirMirrorMount(ctx, rt, g, bareProj)
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

// TestResolveContainer_DegradesWithoutRuntime documents the two-place degrade,
// driven HERMETICALLY through the selectRuntimeProbe seam (never a real
// docker/podman daemon): with a launchable runtime Resolve returns the container
// policy; with no runtime it degrades to None AND records the fatal
// ClassIsolation finding an explicitly-requested-but-unsatisfiable container
// raises.
func TestResolveContainer_DegradesWithoutRuntime(t *testing.T) {
	t.Run("a launchable runtime resolves to the container policy", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeProbe(t, fakeRuntime{name: "docker", available: true})

		p := Resolve(Axes{Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
		assert.Equal(t, "container", p.Name(), "a launchable runtime resolves to the container policy")
		assert.Empty(t, strictness.All(), "a satisfied container request records no finding")
	})

	t.Run("no runtime degrades to none and records one fatal isolation finding", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeProbe(t, Host{})

		p := Resolve(Axes{Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
		assert.Equal(t, "none", p.Name(), "no runtime degrades to none")

		findings := strictness.All()
		require.Len(t, findings, 1, "a requested container with no reachable runtime is one fatal finding")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	})
}
