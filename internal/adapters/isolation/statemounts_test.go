package isolation

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
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

			assert.Equal(t, mount{
				Host:      wantStore,
				Container: path.Join(defaultContainerHome, tt.storeRel),
			}, mounts[0], "persist/transcripts binds to the engine's native store root in the CONTAINER home")
			assert.Equal(t, mount{
				Host:      wantPersist,
				Container: path.Join(defaultContainerHome, ".ctxloom", "sessions", "brisk-teal-otter", "persist"),
			}, mounts[1], "persist/ binds to the container-home session path so in-container artifacts land on the host")
			assert.Equal(t, mount{
				Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
				Container: path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
			}, mounts[2], "THIS project's task log binds into the container home, not the dir holding every project's")
			assert.Equal(t, mount{
				Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl.lock"),
				Container: path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl.lock"),
			}, mounts[3], "the log's lock rides along: a lock the container cannot see excludes nothing")
			assert.Equal(t, mount{
				Host:      wantLocks,
				Container: path.Join(defaultContainerHome, ".ctxloom", "locks"),
			}, mounts[4], "the home-rooted locks dir binds to the container home's .ctxloom/locks — the same directory paths.HomePathFor resolves to when $HOME is the container home, so host and container flock the same inode for an identical-path engine-settings file")

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

// TestSessionStateMounts_UnmappedBackendMountsNoStore: an engine nobody
// declared a container story for maps no transcript store — the fail-closed
// default carries none, because no engine said where its store is — so the
// transcript mount is skipped and every other state mount still applies.
// (Such a run never gets past the auth gate anyway; this pins the mounts
// alone.)
func TestSessionStateMounts_UnmappedBackendMountsNoStore(t *testing.T) {
	testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "unmapped-backend")
	c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)
	require.Len(t, mounts, 4, "persist, task log, its lock, and the locks dir — no transcript store")
	for _, m := range mounts {
		assert.NotContains(t, m.Container, "projects", "no engine store root is guessed for an undeclared engine")
	}
}

// TestSessionStateMounts_NoHarpIsRefused: a container run with no harp has no
// per-session state to scope its mounts to, and is refused rather than degraded
// to a run whose transcripts and artifacts die with the container. Nothing is
// created under the sessions root.
func TestSessionStateMounts_NoHarpIsRefused(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{ProjectID: "proj-1"}
	_, err := c.sessionStateMounts()
	require.Error(t, err, "a harpless container run is refused")

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
func mountReaches(m mount, p string) bool {
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

	spec := runnerSpecFor(Docker{}, "claude-code", t.TempDir(), nil, mounts)
	argv := strings.Join(Docker{}.RunArgs(spec), " ")

	store := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist", "transcripts")
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", store, path.Join(defaultContainerHome, ".claude", "projects")))
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s",
			filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
			path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl")))
	assert.NotContains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", filepath.Join(home, ".ctxloom", "tasks"), path.Join(defaultContainerHome, ".ctxloom", "tasks")),
		"the dir holding every project's task log is never handed to a run")
	assert.NotContains(t, argv, store+",readonly", "the engine writes its transcript store")

	wantLocks, err := paths.HomeLocksDir()
	require.NoError(t, err)
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", wantLocks, path.Join(defaultContainerHome, ".ctxloom", "locks")),
		"the locks-dir mount rides the same --mount argv every other state mount does")
}

// TestSessionStateMounts_LocksDirMount_Unconditional is THE mutation-kill
// target for the lock-path fix: the locks-dir mount must be present even
// when no project id is set — every registered engine spec's
// overlayDirs is non-empty (enginespec.go), so every container run gets an
// engine-settings write mount and needs this facet regardless of session
// identity. Deleting the mount's append call, or gating it behind the harp
// or project-id branches above, makes this go red while leaving every
// harp/project-scoped assertion elsewhere green.
func TestSessionStateMounts_LocksDirMount_Unconditional(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter"}
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)

	wantLocks, err := paths.HomeLocksDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".ctxloom", "locks"), wantLocks)
	assert.Contains(t, mounts, mount{
		Host:      wantLocks,
		Container: path.Join(defaultContainerHome, ".ctxloom", "locks"),
		ReadOnly:  false,
	}, "the locks-dir mount needs no project id")

	info, statErr := os.Stat(wantLocks)
	require.NoError(t, statErr, "the host locks dir must exist before `run`")
	assert.True(t, info.IsDir())
}

// TestWithSessionState_StampsChainPolicies: Prepare's stamping helper carries
// the session identity onto every policy tier that consumes it — the double-stamp
// of a worktree-base Container (Container.state AND the worktree base's ephemeral
// home) included — leaves None alone, and NEVER nil-panics on a bare Container{}
// whose base is nil.
func TestWithSessionState_StampsChainPolicies(t *testing.T) {
	state := SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	chain := withSessionState([]policy{
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

// pathAtOrAboveHome reports whether container-side path target IS the
// container home or a path ANCESTOR of it, comparing cleaned,
// separator-aware path segments rather than raw strings. A naive
// strings.HasPrefix(home, target) gets this wrong in both directions: it
// would call "/home/ctxloomX" an ancestor of "/home/ctxloom" (it shares the
// string prefix but is a SIBLING, not a path ancestor — nothing under
// ctxloomX is under ctxloom), and it would miss "/" as an ancestor of
// everything (root shares no non-trivial string prefix with anything, but
// path-wise it dominates every absolute path). Both are handled here: an
// ancestor must match on a full path SEGMENT boundary, and the root is
// special-cased since target+separator ("//" ) never prefix-matches
// anything.
func pathAtOrAboveHome(target, home string) bool {
	target = filepath.Clean(target)
	home = filepath.Clean(home)
	if target == home {
		return true
	}
	if target == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(home, target+string(filepath.Separator))
}

// TestPathAtOrAboveHome pins the prefix-vs-ancestor distinction the gate
// below depends on: a sibling directory that happens to share a string
// prefix with the home path (ctxloomX vs ctxloom) must NOT read as an
// ancestor, a child of home must NOT read as "at or above" it, and the root
// must read as an ancestor of everything despite sharing no string prefix.
func TestPathAtOrAboveHome(t *testing.T) {
	const home = "/home/ctxloom"
	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{"exact match", "/home/ctxloom", true},
		{"root is an ancestor of everything absolute", "/", true},
		{"direct parent", "/home", true},
		{"trailing slash on target still matches", "/home/ctxloom/", true},
		{"unclean target still matches", "/home/./ctxloom", true},
		{"sibling sharing a string prefix is NOT an ancestor", "/home/ctxloomX", false},
		{"shorter sibling sharing a string prefix is NOT an ancestor", "/home/ctxloo", false},
		{"child of home is BELOW it, not above", "/home/ctxloom/sub", false},
		{"unrelated absolute path", "/var/lib", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pathAtOrAboveHome(tt.target, home))
		})
	}
}

// TestSessionStateMounts_NoMountAtOrAboveContainerHome is the GATE for the
// discipline this file's doc comment states only in PROSE ("Never a blanket
// ~/.ctxloom mount"): container agents are separated from each other by
// NAMESPACE, not by path — every container run gets the identical HOME
// (defaultContainerHome, stamped onto Container by NewContainerFor and onto
// the process by renderRunSpec's -e HOME=...), and is distinct from every
// other only because each has its own filesystem. That boundary rests
// entirely on nothing from the host being mounted AT or ABOVE that shared
// path. A mount landing there would silently merge every container agent's
// home onto one host directory: same path, same code, green suite — nothing
// else in this file would notice.
//
// This walks the REAL mounts sessionStateMounts returns, across every branch
// that changes which mounts it emits (harp+project, harp only) — never a hand-maintained list of expected mounts, which would
// just reproduce the defect this gate exists to catch.
func TestSessionStateMounts_NoMountAtOrAboveContainerHome(t *testing.T) {
	testsupport.Isolate(t)

	states := []SessionState{
		{Harp: "brisk-teal-otter", ProjectID: "proj-1"},
		{Harp: "brisk-teal-otter"},
	}
	for _, state := range states {
		c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
		c.state = state
		mounts, err := c.sessionStateMounts()
		require.NoError(t, err)
		require.NotEmpty(t, mounts, "the locks-dir mount is unconditional and always present")
		for _, m := range mounts {
			assert.False(t, pathAtOrAboveHome(m.Container, c.home),
				"mount target %q sits at or above the container home %q (state=%+v): every container agent shares this home, so this mount would silently merge them into one",
				m.Container, c.home, state)
		}
	}
}

// TestSessionStateMounts_DegradeNoticeCoversEveryAffectedMember pins a fix.
// A review row observed that a missing project id degrades durability behind
// clidiag.WarnOnce, so in a delegated fan-out (agent_run — all one process)
// only the FIRST affected member warns and every later one is silent. The
// mechanism is real: WarnOnce dedups on the whole formatted line and these
// lines carry no member identity.
//
// The collapse is deliberate and stays — the alternative is N identical lines
// at startup — and per-member reporting is not available at this seam anyway:
// Container carries no agent id. What was wrong is that the
// single surviving line described "a container run", singular, so a reader of a
// twenty-member fan-out concluded one member was affected.
//
// Both halves are pinned. The wording is asserted on the notice CONSTANTS
// rather than on captured output, because clidiag's dedup set is process-global
// and a sibling test in this package may legitimately have consumed the line
// first — asserting on the buffer would make this test order-dependent (the
// house workaround, internal/adapters/operations/context_test.go, is a per-test dedup
// key, which is not available for a fixed diagnostic).
func TestSessionStateMounts_DegradeNoticeCoversEveryAffectedMember(t *testing.T) {
	assert.Contains(t, noProjectIDNotice, "every affected run",
		"the one surviving line must say it speaks for every affected member, not read as a single run")

	testsupport.Isolate(t)
	buf := captureWarnings(t)

	// Three fan-out members in one process, none carrying a project id.
	for range 3 {
		c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
		c.state = SessionState{Harp: "brisk-teal-otter"}
		_, err := c.sessionStateMounts()
		require.NoError(t, err, "a member without a project id degrades, it does not fail")
	}

	out := buf.String()
	assert.LessOrEqual(t, strings.Count(out, "no project id"), 1,
		"the project-id degrade collapses per process — one line per member would be startup spam in a fan-out")
}

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
	// container run. The resolver joins with the HOST separator, so its
	// answer is read back as the slash path the Linux container sees.
	testsupport.PointHomeAt(t, defaultContainerHome)
	containerLockPath, err := paths.HomePathFor(protected)
	require.NoError(t, err)
	containerLockPath = filepath.ToSlash(containerLockPath)
	// Restore before touching sessionStateMounts below: that call runs on
	// THIS host process (sessionStateMounts always resolves against the
	// REAL host's $HOME, never the container's — only the mount TARGET
	// names the container path), and would otherwise try to MkdirAll a
	// locks dir under the fake container home on this host's filesystem.
	testsupport.PointHomeAt(t, realHome)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter"}
	require.Equal(t, defaultContainerHome, c.home)
	wantContainerLocksDir := path.Join(c.home, paths.AppDirName, paths.HomeLocksDirName)
	require.True(t, strings.HasPrefix(containerLockPath, wantContainerLocksDir+"/"))

	// The load-bearing equivalence: same basename either side of the
	// boundary, because flattening is a pure function of the protected path.
	assert.Equal(t, filepath.Base(hostLockPath), path.Base(containerLockPath),
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
