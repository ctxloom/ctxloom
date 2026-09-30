//go:build !windows

// Drives writeFakeRuntimeScript's #!/bin/sh runtime stub, which a Windows host cannot exec.

package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hermeticHostContainer is a host-base Container whose whole prepare gate runs
// without a daemon: a fake runtime script that reports the image present and
// provenance-current, stubbed auth, and a stubbed shared-fs probe. The caller
// stamps the session state.
func hermeticHostContainer(t *testing.T, overlayDirs []string) Container {
	t.Helper()
	fake := t.TempDir()
	script := filepath.Join(fake, "fake-docker")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, HostProvenanceDigest(""))
	writeFakeRuntimeScript(t, script, filepath.Join(fake, "builds.log"), fake, labels)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "ctxloom-agent-hermetic-test_latest"), nil, 0o644))

	prevFS := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	return Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-hermetic-test:latest",
		engineSpec: engineContainerSpec{
			engineInstall: []byte("RUN echo fake-install\n"),
			declared:      true,
			overlayDirs:   overlayDirs,
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		base:       hostBase{},
	}
}

// TestContainer_CleanupKeepsOverlayTargets pins the ruling that the overlay
// mountpoints a container run needs inside the LIVE project are created and
// KEPT. They must be pre-created as the invoking user (containerConfigOverlay
// says why), and removing them again at teardown is unsafe: the project is
// shared, so a second run on it mounts the SAME targets, and one run's teardown
// removing an empty target detaches the path the other run's overlay is bound
// to. The accepted cost is an empty .claude/ and .ctxloom/cache/ in a
// container-only project.
//
// Driven through the real PrepareWorkspace and Cleanup, with two workspaces on
// one project: the first creates the targets, and its Cleanup must leave them
// for the second.
func TestContainer_CleanupKeepsOverlayTargets(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()
	targets := []string{".claude", filepath.FromSlash(".ctxloom/cache")}
	c := hermeticHostContainer(t, targets)
	c.state = SessionState{Harp: "brisk-teal-otter"}

	proj := t.TempDir()
	first, err := c.prepareWorkspace(ctx, proj, "member-first")
	require.NoError(t, err)
	for _, rel := range targets {
		require.DirExists(t, filepath.Join(proj, rel), "premise: the first run created the overlay target")
	}
	second, err := c.prepareWorkspace(ctx, proj, "member-second")
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Cleanup() })

	require.NoError(t, first.Cleanup())

	for _, rel := range targets {
		assert.DirExists(t, filepath.Join(proj, rel),
			"the first run's teardown must not remove a target the second run's overlay is still bound to")
	}
}

// TestContainer_ScratchLivesUnderTheSessionEphemeralDir pins where a container
// run's host scratch goes: under the session's ephemeral dir, never the OS temp
// dir. An owner that dies before Cleanup then leaves it inside the session
// layout, where the session's own cleanup reaches it, instead of an orphaned
// ctxloom-iso-* in the temp dir that nothing ever collects.
func TestContainer_ScratchLivesUnderTheSessionEphemeralDir(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	c := hermeticHostContainer(t, []string{".claude"})
	c.state = SessionState{Harp: harp}

	ws, err := c.prepareWorkspace(context.Background(), t.TempDir(), "member-scratch")
	require.NoError(t, err)
	cw := ws.(*containerWorkspace)
	root := cw.scratchRoot

	eph, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	assert.Equal(t, eph, filepath.Dir(root), "the scratch root is a direct child of the session's ephemeral dir")
	assert.True(t, strings.HasPrefix(filepath.Base(root), "ctxloom-iso-"), "scratch root %q keeps its name prefix", root)
	require.DirExists(t, root)

	require.NoError(t, ws.Cleanup())
	assert.NoDirExists(t, root, "Cleanup still removes the scratch root")
}

// TestContainer_HarplessRunIsRefused: a container run with no usable harp has
// nowhere in the session layout to put its scratch, and is refused rather than
// falling back to the OS temp dir. Through the degrade chain the refusal is the
// fatal ClassIsolation finding, the same way an unpreparable state dir fails.
func TestContainer_HarplessRunIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		harp string
		want error
	}{
		"no harp":     {"", errNoSessionHarp},
		"unsafe harp": {"../evil", errUnsafeSessionHarp},
	} {
		t.Run(name, func(t *testing.T) {
			testsupport.Isolate(t)
			resetStrictness(t)
			c := hermeticHostContainer(t, []string{".claude"})
			c.state = SessionState{Harp: tc.harp}
			proj := t.TempDir()

			ws, err := c.resolveWorkspace(context.Background(), proj, "member-harpless")
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, ws)

			mark := strictness.Checkpoint()
			done := captureStderr(t)
			policy, fallback := prepareChain(context.Background(), []policy{c, None{}}, surveyRuntimes(), RuntimeContainerRootless, proj, "member-harpless")
			_ = done()
			found := strictness.Since(mark)
			strictness.Close(mark)
			t.Cleanup(func() { _ = fallback.Cleanup() })

			assert.Equal(t, None{}.Name(), policy.Name(), "the chain walks past the refused container")
			// The hermetic gate records findings of its own (the fake image has
			// no engine recipe and no ctxloom entrypoint); the one under test is
			// the one naming the harp refusal.
			var refusals []report.Finding
			for _, f := range found {
				if strings.Contains(f.Text, tc.want.Error()) {
					refusals = append(refusals, f)
				}
			}
			require.Len(t, refusals, 1, "the refusal is a recorded finding, never a silent host run: %v", found)
			assert.Equal(t, report.KindIsolation, refusals[0].Kind)
			assert.True(t, refusals[0].NonDegradable, "a requested container boundary is refused in both modes")
		})
	}
}

// TestContainerWorktree_FailedMappingDoesNotLeakTheCheckout closes the half of
// the unwind contract that moved when resolution and containerization were
// split. TestWorktreeBase_UnwindsWhatItCreated proves the workspace's Cleanup
// removes the checkout; it says nothing about whether the composed prepare
// actually CALLS that Cleanup when the mapping fails. Deleting that call leaks a
// checkout permanently and leaves the caller a nil workspace with no handle to
// remove it — the precise failure the old prepareBase teardown existed to
// prevent — so it is pinned here, through the real Container.PrepareWorkspace.
func TestContainerWorktree_FailedMappingDoesNotLeakTheCheckout(t *testing.T) {
	ctx := context.Background()

	testsupport.Isolate(t)
	// The mapping fails: the checkout's git common dir cannot be resolved, so no
	// gitdir mirror mount can be built. Resolution has already created the
	// checkout by then, which is what makes this the leak-prone path.
	boom := errors.New("common dir unreadable")
	f := &git.Fake{CommonDirErr: boom}

	c := hermeticHostContainer(t, nil)
	c.base = worktreeBase{wt: NewWorktree(f)}
	c.state = SessionState{Harp: "brisk-teal-otter"}

	ws, err := c.prepareWorkspace(ctx, t.TempDir(), "member-unwind")
	require.Error(t, err, "a mapping that cannot be built must fail the prepare, never launch a broken container")
	assert.ErrorIs(t, err, boom, "the mapping failure must reach the caller intact")
	assert.Nil(t, ws, "a failed prepare hands back no workspace")

	require.Len(t, f.Removed, 1,
		"THE ASSERTION: the checkout resolution created must be torn down by the failed prepare — nothing else holds a handle to it")
	assert.Empty(t, f.Worktrees, "the checkout must not survive the failed prepare")
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
			transcriptStoreRel: ".claude/projects",
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
		Container: path.Join(defaultContainerHome, ".claude", "projects"),
	}, "transcript store mount threaded into the run spec")
	assert.Contains(t, cw.extraMounts, mount{
		Host:      filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
		Container: path.Join(defaultContainerHome, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
	}, "session persist mount threaded into the run spec")
	assert.Contains(t, cw.extraMounts, mount{
		Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
		Container: path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
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
			transcriptStoreRel: ".claude/projects",
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
		Container: path.Join(defaultContainerHome, ".claude", "projects"),
	}, "transcript store mount rides the composition too")
	assert.Contains(t, w.extraMounts, mount{
		Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
		Container: path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
	})
}

// An engine that declared no container story refuses with what is missing as
// the message and what to do as the fix, so every renderer shows the fix
// once, as a fix.
func TestPrepareContainerScratch_UndeclaredContainerNamesItsRemedy(t *testing.T) {
	resetStrictness(t)
	testsupport.Isolate(t)
	c := overrideContainer(t, `{"Entrypoint":null,"User":""}`, "user/own:img").
		WithSessionState(SessionState{Harp: "brisk-teal-otter"})
	c.engineSpec.declared = false

	_, err := c.prepareContainerScratch(context.Background())
	require.Error(t, err)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok)
	assert.Equal(t, noContainerRemedy, fix)
	assert.Contains(t, err.Error(), noContainerHint)
	assert.False(t, strings.Contains(err.Error(), fix), "the remedy is not spliced into the message: %q", err.Error())
}
