package coord

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// TestChildVerbosity pins the env-only diagnostics knob: CTXLOOM_VERBOSE=1
// turns the child plugin/adapter stderr trail on at trace; anything else
// keeps the default-quiet launch.
func TestChildVerbosity(t *testing.T) {
	t.Setenv("CTXLOOM_VERBOSE", "")
	assert.Equal(t, 0, childVerbosity())
	t.Setenv("CTXLOOM_VERBOSE", "1")
	assert.Equal(t, 3, childVerbosity())
}

// TestViaStartRunBackends pins the spawn-cutover gate: every registered
// backend admitted by its own per-backend recon routes its delegated
// children over StartRun.
// Backends NOT reviewed onto the migrated path stay on the FROZEN legacy
// chat path only if legacyChatBackends admits them (since S3b: mock alone);
// any other name is refused at Resolve by checkLegacyChatFreeze — this is a
// deliberate allowlist, not "implements StructuredChat", so a new backend
// never gets swept onto StartRun unreviewed (nor onto the retired legacy
// path at all).
func TestViaStartRunBackends(t *testing.T) {
	cases := map[string]bool{
		"claude-code":  true,
		"mock":         true, // hostable by the binary; see TestProdSpawner_MockIsAdmittedBecauseTheBinaryHostsIt
		"":             false,
		"unknown-type": false,
	}
	for backend, want := range cases {
		assert.Equal(t, want, viaStartRunBackends[backend], "backend %q", backend)
	}
}

// TestResolveResumeMode pins the Slice 2 static per-engine resume-capability
// table (Fork 3's static half): conversational (and the empty/default) always
// resolves to ResumeModePersistent regardless of backend; oneshot resolves to
// ResumeModeOneShot ONLY on a resume-capable backend and otherwise fails
// loud, never silently downgrading to persistent.
func TestResolveResumeMode(t *testing.T) {
	t.Run("conversational is always persistent, any backend", func(t *testing.T) {
		for _, backend := range []string{"claude-code", "mock", "unknown", ""} {
			mode, err := resolveResumeMode(agents.DrivingConversational, backend)
			require.NoError(t, err, "backend %q", backend)
			assert.Equal(t, ResumeModePersistent, mode, "backend %q", backend)
		}
	})

	t.Run("empty driving (the zero value) is persistent", func(t *testing.T) {
		mode, err := resolveResumeMode("", "mock")
		require.NoError(t, err)
		assert.Equal(t, ResumeModePersistent, mode)
	})

	t.Run("oneshot on a resume-capable backend resolves to ResumeModeOneShot", func(t *testing.T) {
		for _, backend := range []string{"claude-code"} {
			mode, err := resolveResumeMode(agents.DrivingOneshot, backend)
			require.NoError(t, err, "backend %q", backend)
			assert.Equal(t, ResumeModeOneShot, mode, "backend %q", backend)
		}
	})

	t.Run("oneshot on a NON-resumable backend FAILS LOUD, never silently downgrades", func(t *testing.T) {
		for _, backend := range []string{"mock", "unknown-backend", ""} {
			mode, err := resolveResumeMode(agents.DrivingOneshot, backend)
			require.Error(t, err, "backend %q", backend)
			assert.Equal(t, ResumeModePersistent, mode, "the returned mode on error must never be ResumeModeOneShot (backend %q)", backend)
			assert.Contains(t, err.Error(), backend)
			assert.Contains(t, err.Error(), "resume-capable")
		}
	})
}

// writeSpawnerConfig (re)writes appDir/config.yaml, exactly like a live
// `ctxloom agent set` (or hand edit) would between two agent_run calls.
func writeSpawnerConfig(t *testing.T, appDir, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))
}

// TestProdSpawner_ResolveRereadsConfigFromDisk is GAP 1's red-then-green
// case: prodSpawner.Resolve must see a NEW agent (or a changed one) written
// to config.yaml AFTER the spawner was constructed, without any restart —
// the captured s.cfg at newProdSpawner time must never be the only source
// consulted for agent-DEFINITION resolution (spawner.go's resolveCfg).
func TestProdSpawner_ResolveRereadsConfigFromDisk(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: plan\n")

	s := newProdSpawner(spawnerApp(t, appDir), filepath.Dir(appDir), nil)

	// The agent present at construction resolves fine (baseline).
	plan, err := s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, "claude-code", plan.Backend)

	// A hot-reload BEFORE any mutation returns the SAME definition (no
	// spurious drift when nothing changed).
	plan, err = s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, "plan", plan.Permission, "the binding's declared posture rides the plan; the launch floors it")

	// The mid-session mutation: config.yaml gains a BRAND NEW agent that
	// never existed in the snapshot captured at newProdSpawner time.
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: plan\n  fresh:\n    llm: claude-code\n    permissions: bypass\n")

	_, err = s.Resolve(context.Background(), "fresh")
	require.NoError(t, err, "a hot-reloaded agent must resolve without a coordinator restart")

	freshPlan, err := s.Resolve(context.Background(), "fresh")
	require.NoError(t, err)
	assert.Equal(t, "claude-code", freshPlan.Backend)
	assert.Equal(t, "bypass", freshPlan.Permission, "the newly-written permission enum resolves, not a stale snapshot")

	// The generation the FIRST spawn captured never mutates: a reload is a
	// new generation, not a rewrite of the one already published.
	_, ok := plan.snap.Config.Agent("fresh")
	assert.False(t, ok, "the first spawn's generation is never rewritten")
}

// TestProdSpawner_ResolveFallsBackToPublishedGenerationOnReloadFailure pins
// the fault-tolerant half: a reload that fails must not break spawning —
// Resolve falls back to the generation already published rather than
// refusing the whole call. The failure is forced by making the config file
// unparsable AFTER the owner opened: the reader refuses a present unparsable
// layer, so the spawn's Reload errors and the published generation serves.
func TestProdSpawner_ResolveFallsBackToPublishedGenerationOnReloadFailure(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: plan\n")

	s := newProdSpawner(spawnerApp(t, appDir), filepath.Dir(appDir), nil)
	writeSpawnerConfig(t, appDir, "version: 6\nagents: [unclosed\n  : nonsense\n")

	plan, err := s.Resolve(context.Background(), "dev")
	require.NoError(t, err, "resolve must not fail outright on a reload read problem")
	assert.Equal(t, "claude-code", plan.Backend, "the startup snapshot still resolves the known agent")
}

// TestProdSpawner_Resolve_Driving is the end-to-end proof at the real
// Spawner.Resolve entry point (a config-key agent, whose only axis validation
// at load is resolveAgentBinding's own ValidateDriving call): the driving axis
// round-trips for conversational/absent, an unknown
// value fails loud, and driving: oneshot fails loud both for a non-resumable
// engine (the permanent capability gate) and for a resume-capable one (the
// separate, deliberately temporary v0.8 "not yet available" gate) — proving
// item 4's requirement is satisfied "regardless" of which one applies.
func TestProdSpawner_Resolve_Driving(t *testing.T) {
	newSpawner := func(t *testing.T, body string) *prodSpawner {
		t.Helper()
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		appDir := filepath.Join(t.TempDir(), ".ctxloom")
		writeSpawnerConfig(t, appDir, body)
		return newProdSpawner(spawnerApp(t, appDir), filepath.Dir(appDir), nil)
	}

	t.Run("absent driving resolves persistent, unchanged from today", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n")
		plan, err := s.Resolve(context.Background(), "dev")
		require.NoError(t, err)
		assert.Equal(t, ResumeModePersistent, plan.ResumeMode)
	})

	t.Run("driving: conversational resolves persistent", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n    driving: conversational\n")
		plan, err := s.Resolve(context.Background(), "dev")
		require.NoError(t, err)
		assert.Equal(t, ResumeModePersistent, plan.ResumeMode)
	})

	t.Run("unknown driving value FAILS LOUD at resolve, not merely at the write edge", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n    driving: bogus\n")
		_, err := s.Resolve(context.Background(), "dev")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bogus")
	})

	t.Run("driving: oneshot on a non-resumable engine fails loud with the capability reason", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: mock\n    permissions: bypass\n    driving: oneshot\n")
		_, err := s.Resolve(context.Background(), "dev")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resume-capable")
		assert.Contains(t, err.Error(), "mock")
	})

	t.Run("driving: oneshot on a SUPPORTED migrated engine (claude-code) now RESOLVES to ResumeModeOneShot (Slice 4 landed)", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n    driving: oneshot\n")
		plan, err := s.Resolve(context.Background(), "dev")
		require.NoError(t, err, "the one-shot turn loop is wired end to end for claude-code (Slice 4)")
		assert.Equal(t, ResumeModeOneShot, plan.ResumeMode)
	})

}

// TestAgentRun_WorkspaceOverrideThreadsToSpawnPlan is GAP 2's threading
// proof at the coordinator layer: the workspace string agent_run's caller
// supplies rides AgentRun -> SpawnPlan.Workspace -> the Spawner's
// Launch/StartEngine call, completely independent of the agent's OWN
// resolved runtime axis (fakeAgent declares no runtime here). Each case gets
// its own coordinator (rather than two AgentRun calls on one) so the D4
// single-slot cap can never queue the second spawn behind the first and
// race this assertion. Omitting the argument (the empty-string call)
// carries nothing — PrepareAgentChat is where an empty override falls back
// to the project's cfg.Workspace default (delegate_test.go pins that hop).
func TestAgentRun_WorkspaceOverrideThreadsToSpawnPlan(t *testing.T) {
	newWorker := func(t *testing.T) (*fakeSpawner, *Coordinator) {
		t.Helper()
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
		return sp, newTestCoordinator(t, sp, nil)
	}

	t.Run("override rides the plan the Spawner launches from", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "worktree", "")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Equal(t, launch.WorkspaceWorktree, sp.lastWorkspace())
	})

	t.Run("omitting workspace carries no override", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Empty(t, sp.lastWorkspace())
	})
}

// TestAgentRun_DirtyTreeHandlerOverrideThreadsToSpawnPlan is the identical
// threading proof as TestAgentRun_WorkspaceOverrideThreadsToSpawnPlan, for
// AgentRun's dirty_tree_handler override: AgentRun -> SpawnPlan.DirtyTreeHandler
// -> the Spawner's Launch/StartEngine call. Omitting it carries nothing —
// PrepareAgentChat is where an empty override falls back to the project's
// cfg.GetDirtyTreeHandler() default (delegate_test.go pins that hop).
func TestAgentRun_DirtyTreeHandlerOverrideThreadsToSpawnPlan(t *testing.T) {
	newWorker := func(t *testing.T) (*fakeSpawner, *Coordinator) {
		t.Helper()
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
		return sp, newTestCoordinator(t, sp, nil)
	}

	t.Run("override rides the plan the Spawner launches from", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", operations.DirtyTreeHandlerStale)
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Equal(t, operations.DirtyTreeHandlerStale, sp.lastDirtyTreeHandler())
	})

	t.Run("omitting dirty_tree_handler carries no override", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Empty(t, sp.lastDirtyTreeHandler())
	})
}
