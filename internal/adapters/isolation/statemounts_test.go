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
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSessionStateMounts_MembersOutputAndTaskLog pins the state mounts: each
// Mounted session member at the same relative path under the CONTAINER home,
// the session's output dir at containerOutputDir with CTXLOOM_OUTPUT_DIR
// naming it, and this project's task log and its lock at the same two paths
// under the container home. Host sources are created as the KIND they are (a
// missing FILE source would be created as a directory by the runtime) and
// every mount is RW.
func TestSessionStateMounts_MembersOutputAndTaskLog(t *testing.T) {
	home := testsupport.Isolate(t)
	const harp = "brisk-teal-otter"

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: harp, ProjectID: "proj-1"}
	mounts, env, err := c.sessionStateMounts()
	require.NoError(t, err)

	members := paths.MountedMembers()
	require.Len(t, mounts, len(members)+3)
	sessionDir := filepath.Join(home, ".ctxloom", "sessions", harp)
	for i, m := range members {
		assert.Equal(t, mount{
			Host:      filepath.Join(sessionDir, filepath.FromSlash(m.Rel())),
			Container: path.Join(defaultContainerHome, ".ctxloom", "sessions", harp, m.Rel()),
		}, mounts[i], "%s binds at its own relative path under the container home", m.Rel())
		info, statErr := os.Stat(mounts[i].Host)
		require.NoError(t, statErr, "bind source %s must exist before `run`", mounts[i].Host)
		assert.Equal(t, m.File, !info.IsDir(), "%s's source is created as the kind it is", m.Rel())
	}
	out, err := fixtureOutputDir(harp)
	require.NoError(t, err)
	assert.Equal(t, mount{Host: out, Container: containerOutputDir}, mounts[len(members)], "the output dir binds where CTXLOOM_OUTPUT_DIR says")
	assert.DirExists(t, out)
	assert.Equal(t, []string{sessions.EnvOutputDir + "=" + containerOutputDir}, env)
	assert.Equal(t, mount{
		Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
		Container: path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl"),
	}, mounts[len(members)+1], "THIS project's task log binds into the container home, not the dir holding every project's")
	assert.Equal(t, mount{
		Host:      filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl.lock"),
		Container: path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl.lock"),
	}, mounts[len(members)+2], "the log's lock rides along: a lock the container cannot see excludes nothing")
	for _, m := range mounts {
		assert.False(t, m.ReadOnly, "state mounts are RW")
		assert.NotEqual(t, sessionDir, m.Host, "the session dir is never mounted whole")
	}
}

// A container run whose session records no output dir is refused: an agent
// told to write its plans there would write them into the container's own
// layer and lose them at teardown.
func TestContainer_ARunWithNoRecordedOutputDirIsRefused(t *testing.T) {
	testsupport.Isolate(t)
	prev := sessionOutputDir
	sessionOutputDir = sessions.OutputDir
	t.Cleanup(func() { sessionOutputDir = prev })

	sidecar, err := paths.HarpSidecarPath("brisk-teal-otter")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))
	require.NoError(t, os.WriteFile(sidecar, []byte("schema_version: 1\nproject_dir: /p\n"), 0o600))

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	_, _, err = c.sessionStateMounts()
	assert.ErrorIs(t, err, sessions.ErrNoOutputDir)
}

// TestSessionStateMounts_NoHarpIsRefused: a container run with no harp has no
// per-session state to scope its mounts to, and is refused rather than degraded
// to a run whose transcripts and artifacts die with the container. Nothing is
// created under the sessions root.
func TestSessionStateMounts_NoHarpIsRefused(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{ProjectID: "proj-1"}
	_, _, err := c.sessionStateMounts()
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
	mounts, _, err := c.sessionStateMounts()
	require.NoError(t, err)
	require.Len(t, mounts, len(paths.MountedMembers())+1, "the session members and the output dir")

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
	mounts, _, err := c.sessionStateMounts()
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
	_, _, err := c.sessionStateMounts()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a safe path segment")
}

// TestSessionStateMounts_RenderedArgv: rendered through the single mount-argv
// site, the state mounts become writable --mount binds (no ,readonly suffix).
func TestSessionStateMounts_RenderedArgv(t *testing.T) {
	home := testsupport.Isolate(t)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	mounts, _, err := c.sessionStateMounts()
	require.NoError(t, err)

	spec := runnerSpecFor(Docker{}, "claude-code", t.TempDir(), nil, mounts)
	argv := strings.Join(mustRunArgs(t, Docker{}, spec), " ")

	spool := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", paths.SpoolDirName)
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", spool, path.Join(defaultContainerHome, ".ctxloom", "sessions", "brisk-teal-otter", paths.SpoolDirName)))
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s",
			filepath.Join(home, ".ctxloom", "tasks", "proj-1.jsonl"),
			path.Join(defaultContainerHome, ".ctxloom", "tasks", "proj-1.jsonl")))
	assert.NotContains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", filepath.Join(home, ".ctxloom", "tasks"), path.Join(defaultContainerHome, ".ctxloom", "tasks")),
		"the dir holding every project's task log is never handed to a run")
	assert.NotContains(t, argv, spool+",readonly", "the spool is written from inside")
}

// TestWithSessionState_StampsChainPolicies: Prepare's stamping helper carries
// the session identity onto every policy tier that consumes it — the double-stamp
// of a worktree-base Container (Container.state AND the worktree base's checkout
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
	assert.Equal(t, state, cw.base.(worktreeBase).wt.state, "worktree base checkout home stamped")
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
		mounts, _, err := c.sessionStateMounts()
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
		_, _, err := c.sessionStateMounts()
		require.NoError(t, err, "a member without a project id degrades, it does not fail")
	}

	out := buf.String()
	assert.LessOrEqual(t, strings.Count(out, "no project id"), 1,
		"the project-id degrade collapses per process — one line per member would be startup spam in a fan-out")
}
