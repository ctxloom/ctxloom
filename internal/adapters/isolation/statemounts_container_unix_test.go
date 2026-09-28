//go:build !windows

// Container isolation has no Windows host support: nothing maps a Windows host path into the Linux container.

package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestHomePathFor_ContainerHomeResolvesUnderMountedLocksDir pins the
// path-resolution equivalence the lock-path fix depends on, WITHOUT a live
// container: paths.HomePathFor, invoked as if $HOME were the container's
// fresh home (the -e HOME=<container home> every container run sets — see
// renderRunSpec), must resolve an identical-path engine-settings file's lock
// sidecar to a path directly under this Container's locks-dir mount target.
// That is precisely what makes the mount fix work: the flattened lock
// filename depends only on the PROTECTED file's absolute path (identical on
// both sides of the boundary for a same-path engine-settings mount), never
// on which $HOME computed it, so the same host directory holds the file both
// the host process and the in-container process open.
//
// A real container run of this proof is deferred to the docker-gated lane
// (statemounts_docker_integration_test.go's
// TestContainerLockMount_HostAndContainerReadSameLockFile) — this test
// covers the pure path arithmetic without requiring a docker daemon.
func TestHomePathFor_ContainerHomeResolvesUnderMountedLocksDir(t *testing.T) {
	realHome := testsupport.Isolate(t)
	projectDir := t.TempDir()
	protected := filepath.Join(projectDir, ".claude", "settings.json")

	// The host side: paths.HomePathFor under the REAL (isolated) host home.
	hostLockPath, err := paths.HomePathFor(protected)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hostLockPath, filepath.Join(realHome, ".ctxloom", "locks")+string(filepath.Separator)))

	// The container side: the SAME resolver, called with $HOME temporarily
	// repointed at the container's fresh home -- exactly what runs inside the
	// container, since renderRunSpec sets HOME=defaultContainerHome for every
	// container run.
	t.Setenv("HOME", defaultContainerHome)
	containerLockPath, err := paths.HomePathFor(protected)
	require.NoError(t, err)
	// Restore before touching sessionStateMounts below: that call runs on
	// THIS host process (sessionStateMounts always resolves against the
	// REAL host's $HOME, never the container's — only the mount TARGET
	// names the container path), and would otherwise try to MkdirAll a
	// locks dir under the fake container home on this host's filesystem.
	t.Setenv("HOME", realHome)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter"}
	require.Equal(t, defaultContainerHome, c.home)
	wantContainerLocksDir := filepath.Join(c.home, paths.AppDirName, paths.HomeLocksDirName)
	require.True(t, strings.HasPrefix(containerLockPath, wantContainerLocksDir+string(filepath.Separator)))

	// The load-bearing equivalence: same basename either side of the
	// boundary, because flattening is a pure function of the protected path.
	assert.Equal(t, filepath.Base(hostLockPath), filepath.Base(containerLockPath),
		"host and container HomePathFor must derive the IDENTICAL lock filename for the same protected path")

	// And the mount this package builds carries exactly that container
	// target as its Container side, and the host locks dir (not the
	// container's) as its Host side.
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)
	var found bool
	for _, m := range mounts {
		if m.Container == wantContainerLocksDir {
			found = true
			assert.Equal(t, filepath.Join(realHome, ".ctxloom", "locks"), m.Host,
				"the mount's host side must be the REAL host locks dir, not the container's")
		}
	}
	assert.True(t, found, "sessionStateMounts must carry a mount whose container target is the container-home locks dir")
}

// TestContainerPrepareWorkspace_ThreadsStateMounts drives the FULL container
// prepare gate hermetically (fake runtime script marks the image present and
// provenance-current, stubbed shared-fs probe, stubbed auth) and pins that the
// prepared workspace's extraMounts carry the session-state mounts alongside
// the auth mounts — the wiring an argv-only unit test can't see.
func TestContainerPrepareWorkspace_ThreadsStateMounts(t *testing.T) {
	home := testsupport.Isolate(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-docker")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, HostProvenanceDigest(""))
	writeFakeRuntimeScript(t, script, filepath.Join(dir, "builds.log"), dir, labels)
	// Pre-mark the image present (the script's marker convention: image name
	// with '/' and ':' mapped to '_').
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ctxloom-agent-state-test_latest"), nil, 0o644))

	prevFS := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	c := Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-state-test:latest",
		engineSpec: engineContainerSpec{
			engineInstall:      []byte("RUN echo fake-install\n"), // buildable → the run-as-is identity inspect is skipped
			declared:           true,
			overlayDirs:        []string{".claude"},
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		state:      SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"},
		base:       hostBase{},
	}

	ws, err := c.prepareWorkspace(context.Background(), t.TempDir(), "member-x")
	require.NoError(t, err)
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok)
	t.Cleanup(func() { _ = cw.Cleanup() })
	requireCleanWorkspace(t, ws)

	store := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist", "transcripts")
	assert.Contains(t, cw.extraMounts, mount{
		Host:      store,
		Container: filepath.Join(defaultContainerHome, ".claude", "projects"),
	}, "transcript store mount threaded into the run spec")
	assert.Contains(t, cw.extraMounts, mount{
		Host:      filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
	}, "session persist mount threaded into the run spec")
	assert.Contains(t, cw.extraMounts, mount{
		Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
	}, "this project's task-log mount threaded into the run spec")
}

// TestContainerWorktreePrepareWorkspace_ThreadsStateMounts: the
// worktree-in-container composition carries the same state mounts (they hang
// off the shared container scratch, not the workspace flavor).
func TestContainerWorktreePrepareWorkspace_ThreadsStateMounts(t *testing.T) {
	home := testsupport.Isolate(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-docker")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, HostProvenanceDigest(""))
	writeFakeRuntimeScript(t, script, filepath.Join(dir, "builds.log"), dir, labels)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ctxloom-agent-state-test_latest"), nil, 0o644))

	prevFS := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	cw := Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-state-test:latest",
		engineSpec: engineContainerSpec{
			engineInstall:      []byte("RUN echo fake-install\n"),
			declared:           true,
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		state:      SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"},
		base:       worktreeBase{wt: NewWorktree(&git.Fake{CommonDirValue: t.TempDir()})},
	}

	ws, err := cw.prepareWorkspace(context.Background(), "/proj", "member-x")
	require.NoError(t, err)
	w, ok := ws.(*containerWorkspace)
	require.True(t, ok)
	t.Cleanup(func() { _ = w.Cleanup() })
	requireCleanWorkspace(t, ws)
	// requireCleanWorkspace's *containerWorkspace case only reaches
	// scratchRoot: the composed worktree base's own config-home
	// (provisionConfigHome, real even under git.Fake — see cleanupConfigHome's
	// doc) is buried behind the opaque baseCleanup closure with no typed way
	// to reach it from here. It's never mounted/used inside the container,
	// so sweep it by its deterministic prefix rather than leaving it to whatever mutant hits
	// w.Cleanup()'s removal logic.
	t.Cleanup(func() {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "ctxloom-cfg-member-x-*"))
		for _, m := range matches {
			_ = os.RemoveAll(m)
		}
	})

	assert.Contains(t, w.extraMounts, mount{
		Host:      filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist", "transcripts"),
		Container: filepath.Join(defaultContainerHome, ".claude", "projects"),
	}, "transcript store mount rides the composition too")
	assert.Contains(t, w.extraMounts, mount{
		Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
	})
}
