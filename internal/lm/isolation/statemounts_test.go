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

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSessionStateFromEnv: the session identity reads from the SAME env map
// the launch paths export into the engine (harp + project id); absent keys
// yield zero fields, and a nil map is safe.
func TestSessionStateFromEnv(t *testing.T) {
	got := SessionStateFromEnv(map[string]string{
		"CTXLOOM_SESSION_HARP": "brisk-teal-otter",
		"CTXLOOM_PROJECT_ID":   "proj-1",
		"OTHER":                "ignored",
	})
	assert.Equal(t, SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}, got)

	assert.Equal(t, SessionState{}, SessionStateFromEnv(nil))
	assert.Equal(t, SessionState{Harp: "h"}, SessionStateFromEnv(map[string]string{"CTXLOOM_SESSION_HARP": "h"}))
}

// TestSessionStateMounts_PerBackendStoreRoots pins the per-backend transcript
// store map (§6b L1): the harp's persist/transcripts dir bind-mounts RW to
// each engine's native STORE ROOT resolved against the CONTAINER home, the
// persist dir maps to the container-home ~/.ctxloom session path, this
// project's task log and its lock map to the same two paths under the
// container home, and the home-rooted locks dir maps to the container home's
// .ctxloom/locks (the engine-settings lock-path fix). Host sources are
// created (a bind source must exist, and a missing FILE source would be
// created as a directory by the runtime) and every mount is RW.
func TestSessionStateMounts_PerBackendStoreRoots(t *testing.T) {
	tests := []struct {
		backend  string
		storeRel string
	}{
		{"claude-code", ".claude/projects"},
		{"codex", ".codex/sessions"},
		{"kiro", ".kiro"},
		{"unmapped-backend", ".claude/projects"}, // default spec is claude-oriented
	}
	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			home := testsupport.Isolate(t)

			c := NewContainerFor(fakeRuntime{name: "docker", available: true}, tt.backend)
			c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
			mounts, err := c.sessionStateMounts()
			require.NoError(t, err)
			require.Len(t, mounts, 5)

			wantStore, err := paths.HarpTranscriptStoreDir("brisk-teal-otter")
			require.NoError(t, err)
			wantPersist, err := paths.HarpPersistDir("brisk-teal-otter")
			require.NoError(t, err)
			wantLocks, err := paths.HomeLocksDir()
			require.NoError(t, err)

			assert.Equal(t, Mount{
				Host:      wantStore,
				Container: filepath.Join(defaultContainerHome, filepath.FromSlash(tt.storeRel)),
			}, mounts[0], "persist/transcripts binds to the engine's native store root in the CONTAINER home")
			assert.Equal(t, Mount{
				Host:      wantPersist,
				Container: filepath.Join(defaultContainerHome, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
			}, mounts[1], "persist/ binds to the container-home session path so in-container artifacts land on the host")
			assert.Equal(t, Mount{
				Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
				Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
			}, mounts[2], "THIS project's task log binds into the container home, not the dir holding every project's")
			assert.Equal(t, Mount{
				Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl.lock"),
				Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl.lock"),
			}, mounts[3], "the log's lock rides along: a lock the container cannot see excludes nothing")
			assert.Equal(t, Mount{
				Host:      wantLocks,
				Container: filepath.Join(defaultContainerHome, ".ctxloom", "locks"),
			}, mounts[4], "the home-rooted locks dir binds to the container home's .ctxloom/locks — the same directory filelock.HomePathFor resolves to when $HOME is the container home, so host and container flock the same inode for an identical-path engine-settings file")

			for _, m := range mounts {
				assert.False(t, m.ReadOnly, "state mounts are RW: the engine/taskloom writes them")
				info, statErr := os.Stat(m.Host)
				require.NoError(t, statErr, "bind source %s must exist before `run`", m.Host)
				assert.Equal(t, strings.HasSuffix(m.Host, ".jsonl") || strings.HasSuffix(m.Host, ".lock"), !info.IsDir(),
					"the task sources are FILES and the session sources are DIRS; a missing file source is created as a directory by the runtime")
			}
		})
	}
}

// TestSessionStateMounts_NoHarp_SkipsPerSessionMounts pins the no-harp edge:
// mounts that need a session identity degrade to skipped-with-warning (an
// ensemble member without per-member session accounting runs this way by
// design), while the harp-independent task-store mount still applies. Nothing
// is created under the sessions root.
func TestSessionStateMounts_NoHarp_SkipsPerSessionMounts(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{ProjectID: "proj-1"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)
	require.Len(t, mounts, 3, "the task-store mounts (log + lock) plus the harp-independent locks-dir mount apply without a harp")
	assert.Equal(t, filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"), mounts[0].Host)
	assert.Equal(t, filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl.lock"), mounts[1].Host)
	wantLocks, err := paths.HomeLocksDir()
	require.NoError(t, err)
	assert.Equal(t, wantLocks, mounts[2].Host, "the locks-dir mount needs no session identity")

	_, statErr := os.Stat(filepath.Join(home, ".ctxloom", "sessions"))
	assert.True(t, os.IsNotExist(statErr), "no session dir is minted for a harpless run")
}

// TestSessionStateMounts_NoProjectID_SkipsTaskMount: without a pinned project
// id the in-container taskloom would mint a fresh one and write a
// wrongly-keyed log, so the shared-store mount is skipped — the per-session
// mounts still apply.
func TestSessionStateMounts_NoProjectID_SkipsTaskMount(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)
	require.Len(t, mounts, 3, "transcript store + persist, plus the project-independent locks-dir mount")
	wantLocks, err := paths.HomeLocksDir()
	require.NoError(t, err)
	assert.Equal(t, wantLocks, mounts[2].Host, "the locks-dir mount needs no project id")

	_, statErr := os.Stat(filepath.Join(home, ".ctxloom", "tasks"))
	assert.True(t, os.IsNotExist(statErr), "no shared task dir is minted without a project id")
}

// A container run gets ONE project's task log, never the home-rooted dir that
// holds every project's. The task store is home-scoped and shared by every
// project on the machine, so the directory mount handed a run for project A
// read-write access to project B's task log — a project it has no relationship
// with, whose tasks it can read and whose log it can append to or corrupt.
// Nothing needed that: the run writes one file, the one its pinned project id
// names.
func TestSessionStateMounts_TaskMountReachesOnlyThisProjectsLog(t *testing.T) {
	home := testsupport.Isolate(t)

	tasksDir := filepath.Join(home, ".ctxloom", "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))
	otherLog := filepath.Join(tasksDir, "proj-b.jsonl")
	require.NoError(t, os.WriteFile(otherLog, []byte("{\"op\":\"add\"}\n"), 0o644))
	ownLog := filepath.Join(tasksDir, "proj-a.jsonl")

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-a"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)

	var reachesOwn bool
	for _, m := range mounts {
		assert.False(t, mountReaches(m, otherLog),
			"mount %s → %s hands this run another project's task log (%s)", m.Host, m.Container, otherLog)
		reachesOwn = reachesOwn || mountReaches(m, ownLog)
	}
	assert.True(t, reachesOwn,
		"the run's OWN task log must still reach the host store: least privilege is a narrower mount, not no mount")
}

// mountReaches reports whether host path p is inside (or is) what m exposes to
// the container.
func mountReaches(m Mount, p string) bool {
	return p == m.Host || strings.HasPrefix(p, m.Host+string(filepath.Separator))
}

// TestSessionStateMounts_RejectsUnsafeHarp: the harp arrives from an env map
// and becomes both a host path and a bind source, so a non-segment value is a
// preparation error (→ the caller's fatal-unless-degraded degrade), never a
// path traversal.
func TestSessionStateMounts_RejectsUnsafeHarp(t *testing.T) {
	testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "../evil", ProjectID: "proj-1"}
	_, err := c.sessionStateMounts()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a safe path segment")
}

// TestSessionStateMounts_RenderedArgv: rendered through the single mount-argv
// site, the state mounts become writable --mount binds (no ,readonly suffix).
func TestSessionStateMounts_RenderedArgv(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)

	spec := buildRunSpec("img", "name", "/proj", defaultContainerHome,
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "claude-code"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin123",
		nil, nil, mounts, nil)
	argv := strings.Join(Docker{}.RunArgs(spec), " ")

	store := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist", "transcripts")
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", store, filepath.Join(defaultContainerHome, ".claude", "projects")))
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s",
			filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
			filepath.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl")))
	assert.NotContains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", filepath.Join(home, ".ctxloom", "tasks"), filepath.Join(defaultContainerHome, ".ctxloom", "tasks")),
		"the dir holding every project's task log is never handed to a run")
	assert.NotContains(t, argv, store+",readonly", "the engine writes its transcript store")

	wantLocks, err := paths.HomeLocksDir()
	require.NoError(t, err)
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", wantLocks, filepath.Join(defaultContainerHome, ".ctxloom", "locks")),
		"the locks-dir mount rides the same --mount argv every other state mount does")
}

// TestSessionStateMounts_LocksDirMount_Unconditional is THE mutation-kill
// target for the lock-path fix: the locks-dir mount must be present even
// when NEITHER harp nor project id is set — every registered engine spec's
// overlayDirs is non-empty (enginespec.go), so every container run gets an
// engine-settings write mount and needs this facet regardless of session
// identity. Deleting the mount's append call, or gating it behind the harp
// or project-id branches above, makes this go red while leaving every
// harp/project-scoped assertion elsewhere green.
func TestSessionStateMounts_LocksDirMount_Unconditional(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)
	require.Len(t, mounts, 1, "no harp, no project id -- only the unconditional locks-dir mount survives")

	wantLocks, err := paths.HomeLocksDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".ctxloom", "locks"), wantLocks)
	assert.Equal(t, Mount{
		Host:      wantLocks,
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "locks"),
		ReadOnly:  false,
	}, mounts[0])

	info, statErr := os.Stat(wantLocks)
	require.NoError(t, statErr, "the host locks dir must exist before `run`")
	assert.True(t, info.IsDir())
}

// TestHomePathFor_ContainerHomeResolvesUnderMountedLocksDir pins the
// path-resolution equivalence the lock-path fix depends on, WITHOUT a live
// container: filelock.HomePathFor, invoked as if $HOME were the container's
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

	// The host side: filelock.HomePathFor under the REAL (isolated) host home.
	hostLockPath, err := filelock.HomePathFor(protected)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hostLockPath, filepath.Join(realHome, ".ctxloom", "locks")+string(filepath.Separator)))

	// The container side: the SAME resolver, called with $HOME temporarily
	// repointed at the container's fresh home -- exactly what runs inside the
	// container, since renderRunSpec sets HOME=defaultContainerHome for every
	// container run.
	t.Setenv("HOME", defaultContainerHome)
	containerLockPath, err := filelock.HomePathFor(protected)
	require.NoError(t, err)
	// Restore before touching sessionStateMounts below: that call runs on
	// THIS host process (sessionStateMounts always resolves against the
	// REAL host's $HOME, never the container's — only the mount TARGET
	// names the container path), and would otherwise try to MkdirAll a
	// locks dir under the fake container home on this host's filesystem.
	t.Setenv("HOME", realHome)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
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

// TestWithSessionState_StampsChainPolicies: Prepare's stamping helper carries
// the session identity onto every policy tier that consumes it — the double-stamp
// of a worktree-base Container (Container.state AND the worktree base's ephemeral
// home) included — leaves None alone, and NEVER nil-panics on a bare Container{}
// whose base is nil.
func TestWithSessionState_StampsChainPolicies(t *testing.T) {
	state := SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	chain := withSessionState([]Policy{
		Container{base: worktreeBase{wt: Worktree{}}},
		Container{}, // bare, nil base — the nil-base guard must not panic
		Worktree{},
		None{},
	}, state)

	cw := chain[0].(Container)
	assert.Equal(t, state, cw.state, "container durable-state stamped")
	assert.Equal(t, state, cw.base.(worktreeBase).wt.state, "worktree base ephemeral home stamped")
	assert.Equal(t, state, chain[1].(Container).state, "a bare Container's state stamps without a base")
	assert.Equal(t, state, chain[2].(Worktree).state)
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
			engineInstall: []byte("RUN echo fake-install\n"), // buildable → the run-as-is identity inspect is skipped
			resolveAuth: func(string, string) (containerAuth, bool) {
				return containerAuth{mode: authEnv, envPassthrough: []string{"X"}}, true
			},
			overlayDirs:        []string{".claude"},
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		socketDir:  defaultContainerSocketDir,
		state:      SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"},
		base:       hostBase{},
	}

	ws, err := c.PrepareWorkspace(context.Background(), t.TempDir(), "member-x")
	require.NoError(t, err)
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok)
	t.Cleanup(func() { _ = cw.Cleanup() })
	requireCleanWorkspace(t, ws)

	store := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist", "transcripts")
	assert.Contains(t, cw.extraMounts, Mount{
		Host:      store,
		Container: filepath.Join(defaultContainerHome, ".claude", "projects"),
	}, "transcript store mount threaded into the run spec")
	assert.Contains(t, cw.extraMounts, Mount{
		Host:      filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
	}, "session persist mount threaded into the run spec")
	assert.Contains(t, cw.extraMounts, Mount{
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
			engineInstall: []byte("RUN echo fake-install\n"),
			resolveAuth: func(string, string) (containerAuth, bool) {
				return containerAuth{mode: authEnv}, true
			},
			transcriptStoreRel: filepath.FromSlash(".claude/projects"),
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		socketDir:  defaultContainerSocketDir,
		state:      SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"},
		base:       worktreeBase{wt: NewWorktree(&git.Fake{CommonDirValue: t.TempDir()}, "")},
	}

	ws, err := cw.PrepareWorkspace(context.Background(), "/proj", "member-x")
	require.NoError(t, err)
	w, ok := ws.(*containerWorkspace)
	require.True(t, ok)
	t.Cleanup(func() { _ = w.Cleanup() })
	requireCleanWorkspace(t, ws)
	// requireCleanWorkspace's *containerWorkspace case only reaches
	// scratchRoot: the composed worktree base's own config-home
	// (provisionConfigHome, real even under git.Fake — see cleanupConfigHome's
	// doc) is buried behind the opaque baseCleanup closure with no typed way
	// to reach it from here. It's never mounted/used inside the container
	// (TestContainerWorktreeWorkspace_NoConfigHomeEnv), so sweep it by its
	// deterministic prefix rather than leaving it to whatever mutant hits
	// w.Cleanup()'s removal logic.
	t.Cleanup(func() {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "ctxloom-cfg-member-x-*"))
		for _, m := range matches {
			_ = os.RemoveAll(m)
		}
	})

	assert.Contains(t, w.extraMounts, Mount{
		Host:      filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist", "transcripts"),
		Container: filepath.Join(defaultContainerHome, ".claude", "projects"),
	}, "transcript store mount rides the composition too")
	assert.Contains(t, w.extraMounts, Mount{
		Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
	})
}

// TestSessionStateMounts_DegradeNoticeCoversEveryAffectedMember pins a fix.
// A review row observed that a missing harp or project id degrades durability behind
// clidiag.WarnOnce, so in a delegated fan-out (agent_run — all one process)
// only the FIRST affected member warns and every later one is silent. The
// mechanism is real: WarnOnce dedups on the whole formatted line and these
// lines carry no member identity.
//
// The collapse is deliberate and stays — the alternative is N identical lines
// at startup — and per-member reporting is not available at this seam anyway:
// Container carries no agent id, and the identity that would distinguish
// members is precisely the harp that is missing. What was wrong is that the
// single surviving line described "a container run", singular, so a reader of a
// twenty-member fan-out concluded one member was affected.
//
// Both halves are pinned. The wording is asserted on the notice CONSTANTS
// rather than on captured output, because clidiag's dedup set is process-global
// and a sibling test in this package may legitimately have consumed the line
// first — asserting on the buffer would make this test order-dependent (the
// house workaround, internal/operations/context_test.go, is a per-test dedup
// key, which is not available for a fixed diagnostic).
func TestSessionStateMounts_DegradeNoticeCoversEveryAffectedMember(t *testing.T) {
	for _, notice := range []string{noHarpNotice, noProjectIDNotice} {
		assert.Contains(t, notice, "every affected run",
			"the one surviving line must say it speaks for every affected member, not read as a single run: %q", notice)
	}

	testsupport.Isolate(t)
	buf := captureWarnings(t)

	// Three fan-out members in one process, none carrying session identity.
	for range 3 {
		c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
		_, err := c.sessionStateMounts()
		require.NoError(t, err, "a member without session accounting degrades, it does not fail")
	}

	out := buf.String()
	assert.LessOrEqual(t, strings.Count(out, "no session harp"), 1,
		"the harp degrade collapses per process — one line per member would be startup spam in a fan-out")
	assert.LessOrEqual(t, strings.Count(out, "no project id"), 1,
		"the project-id degrade collapses the same way")
}
