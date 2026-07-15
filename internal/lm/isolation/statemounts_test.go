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
// persist dir maps to the container-home ~/.ctxloom session path, and the
// shared task-log dir maps to the container-home ~/.ctxloom/tasks. Host
// sources are created (a bind source must exist) and every mount is RW.
func TestSessionStateMounts_PerBackendStoreRoots(t *testing.T) {
	tests := []struct {
		backend  string
		storeRel string
	}{
		{"claude-code", ".claude/projects"},
		{"codex", ".codex/sessions"},
		{"kiro", ".kiro"},
		{"antigravity", ".gemini/antigravity-cli/brain"},
		{"unmapped-backend", ".claude/projects"}, // default profile is claude-oriented
	}
	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			home := testsupport.Isolate(t)

			c := NewContainerFor(fakeRuntime{name: "docker", available: true}, tt.backend)
			c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
			mounts, err := c.sessionStateMounts()
			require.NoError(t, err)
			require.Len(t, mounts, 3)

			wantStore, err := paths.HarpTranscriptStoreDir("brisk-teal-otter")
			require.NoError(t, err)
			wantPersist, err := paths.HarpPersistDir("brisk-teal-otter")
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
				Host:      filepath.Join(home, ".ctxloom", "tasks"),
				Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks"),
			}, mounts[2], "the project-shared task-log dir binds into the container home")

			for _, m := range mounts {
				assert.False(t, m.ReadOnly, "state mounts are RW: the engine/taskloom writes them")
				info, statErr := os.Stat(m.Host)
				require.NoError(t, statErr, "bind source %s must exist before `run`", m.Host)
				assert.True(t, info.IsDir())
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
	require.Len(t, mounts, 1, "only the task-store mount applies without a harp")
	assert.Equal(t, filepath.Join(home, ".ctxloom", "tasks"), mounts[0].Host)

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
	require.Len(t, mounts, 2, "transcript store + persist only")

	_, statErr := os.Stat(filepath.Join(home, ".ctxloom", "tasks"))
	assert.True(t, os.IsNotExist(statErr), "no shared task dir is minted without a project id")
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
		nil, nil, mounts, 0)
	argv := strings.Join(Docker{}.RunArgs(spec), " ")

	store := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "persist", "transcripts")
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", store, filepath.Join(defaultContainerHome, ".claude", "projects")))
	assert.Contains(t, argv,
		fmt.Sprintf("--mount type=bind,source=%s,target=%s", filepath.Join(home, ".ctxloom", "tasks"), filepath.Join(defaultContainerHome, ".ctxloom", "tasks")))
	assert.NotContains(t, argv, store+",readonly", "the engine writes its transcript store")
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
	sharedFSCheck = func(context.Context, Runtime, string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	c := Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-state-test:latest",
		profile: containerProfile{
			officialImage: "example/client:1", // buildable → the run-as-is identity inspect is skipped
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
		Host:      filepath.Join(home, ".ctxloom", "tasks"),
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks"),
	}, "shared task-store mount threaded into the run spec")
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
	sharedFSCheck = func(context.Context, Runtime, string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	cw := Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-state-test:latest",
		profile: containerProfile{
			officialImage: "example/client:1",
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
		Host:      filepath.Join(home, ".ctxloom", "tasks"),
		Container: filepath.Join(defaultContainerHome, ".ctxloom", "tasks"),
	})
}
