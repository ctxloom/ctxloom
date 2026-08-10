package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// managerTestDir isolates the environment and returns a fresh, real (OS
// filesystem) .ctxloom directory for a Manager to operate on. A real
// filesystem is required, not afero.NewMemMapFs: Manager.Update's advisory
// filelock is skipped entirely for an injected test fs (no cross-process
// readers to protect), so the lock-holding tests below need real files.
func managerTestDir(t *testing.T) string {
	t.Helper()
	home := testsupport.Isolate(t)
	appDir := filepath.Join(home, "project", AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	Invalidate()
	t.Cleanup(Invalidate)
	return appDir
}

// TestUpdate_SerializesConcurrentWritersInProcess proves N goroutines each
// Update-ing a DISTINCT key all survive: none is silently lost to another's
// concurrent read-modify-write, which is exactly the failure mode a
// SetAgent-shaped direct mutation of a shared, staleness-prone *Config was
// exposed to before Manager/Draft existed.
func TestUpdate_SerializesConcurrentWritersInProcess(t *testing.T) {
	appDir := managerTestDir(t)
	mgr := NewManager(WithAppDir(appDir))

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = mgr.Update(func(d *Draft) error {
				if d.Agents == nil {
					d.Agents = map[string]agents.Agent{}
				}
				d.Agents[fmt.Sprintf("agent-%02d", i)] = agents.Agent{LLM: fmt.Sprintf("engine-%02d", i)}
				return nil
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "writer %d", i)
	}

	final, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	got := final.GetConfiguredAgents()
	require.Len(t, got, n, "every concurrent writer's distinct key must survive — a lost write means Update did not actually serialize the read-modify-write span")
	for i := 0; i < n; i++ {
		assert.Contains(t, got, fmt.Sprintf("agent-%02d", i))
	}
}

// TestUpdate_FailsClosedWhenLockCannotBeAcquired pins Manager.Update's half
// of the fail-closed-on-lock-failure fix: a lock ACQUISITION failure (as
// opposed to blocking on contention, which filelock.Lock already handles by
// waiting) used to degrade to an unlocked read-modify-write, silently.
// Forced here by making the lock file's path a pre-existing directory so
// os.OpenFile(O_CREATE|O_RDWR) fails outright, with no contention involved.
func TestUpdate_FailsClosedWhenLockCannotBeAcquired(t *testing.T) {
	appDir := managerTestDir(t)
	mgr := NewManager(WithAppDir(appDir))

	pathCfg, err := loadUncached(mgr.opts...)
	require.NoError(t, err)
	configPath, err := pathCfg.GetConfigFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(configPath+".lock", 0o755))

	err = mgr.Update(func(d *Draft) error {
		d.DefaultAgent = "should-not-be-written"
		return nil
	})
	require.Error(t, err, "Update must fail closed rather than silently update unlocked when the lock cannot be acquired")
}

// TestUpdate_HoldsFileLockAcrossReadModifyWrite is the lost-update guard:
// it proves the lock spans the WHOLE transaction, not just the final write.
// Writer A is parked mid-transaction (inside fn, lock held); writer B's
// Update call is started concurrently and must be UNABLE to complete while A
// holds the lock — if Update only locked around the write, B could read stale pre-A
// state and complete before A ever commits.
// Once A is released, B's fn must observe A's committed change (the fresh
// reload happens AFTER the lock is acquired), and both changes must survive.
func TestUpdate_HoldsFileLockAcrossReadModifyWrite(t *testing.T) {
	appDir := managerTestDir(t)
	mgr := NewManager(WithAppDir(appDir))

	aEnteredCritical := make(chan struct{})
	releaseA := make(chan struct{})
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		err := mgr.Update(func(d *Draft) error {
			close(aEnteredCritical)
			<-releaseA
			if d.Agents == nil {
				d.Agents = map[string]agents.Agent{}
			}
			d.Agents["a"] = agents.Agent{LLM: "a"}
			return nil
		})
		assert.NoError(t, err)
	}()
	<-aEnteredCritical // A now holds the lock, blocked inside fn.

	bSawA := make(chan bool, 1)
	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		err := mgr.Update(func(d *Draft) error {
			_, ok := d.Agents["a"]
			bSawA <- ok
			d.Agents["b"] = agents.Agent{LLM: "b"}
			return nil
		})
		assert.NoError(t, err)
	}()

	// B's Update call is now racing against A's lock. Give it every chance to
	// (wrongly) complete while A still holds the lock.
	select {
	case <-bDone:
		t.Fatal("writer B's Update completed while writer A still held the lock — " +
			"Update is not serializing the full read-modify-write span, only some part of it")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseA)
	<-aDone
	<-bDone

	require.True(t, <-bSawA, "writer B's fresh reload (taken AFTER acquiring the lock) must see writer A's already-committed change")

	final, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	got := final.GetConfiguredAgents()
	assert.Contains(t, got, "a")
	assert.Contains(t, got, "b")
}

// TestConfig_SaveLockedAloneLosesConcurrentWrites is the empirical negative
// control for TestUpdate_SerializesConcurrentWritersInProcess above: it
// reproduces the EXACT shape every write call site used before migrating
// onto Manager.Update — LoadFresh, mutate the in-memory copy, write directly
// via saveLocked with no re-read-under-lock — and proves it silently loses
// writes. n goroutines each capture a fresh snapshot BEFORE any of them
// takes a lock, so the read-merge-write only ever merges each goroutine's own
// single added key onto whatever was on disk at THAT goroutine's read time —
// not onto whatever a prior writer just committed. This is exactly why
// Manager.Update re-reads fresh AFTER acquiring the lock instead of merging a
// pre-lock snapshot (saveLocked itself takes no lock at all — callers, like
// Manager.Update, are responsible for their own).
func TestConfig_SaveLockedAloneLosesConcurrentWrites(t *testing.T) {
	appDir := managerTestDir(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg, err := LoadFresh(WithAppDir(appDir))
			if err != nil {
				errs[i] = err
				return
			}
			if cfg.agents == nil {
				cfg.agents = map[string]agents.Agent{}
			}
			cfg.agents[fmt.Sprintf("agent-%02d", i)] = agents.Agent{LLM: fmt.Sprintf("engine-%02d", i)}
			configPath, perr := cfg.GetConfigFilePath()
			if perr != nil {
				errs[i] = perr
				return
			}
			errs[i] = cfg.saveLocked(cfg.getFS(), configPath)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "writer %d", i)
	}

	final, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	got := final.GetConfiguredAgents()

	t.Logf("bare LoadFresh-mutate-saveLocked: %d of %d concurrent writes survived (%d lost)", len(got), n, n-len(got))
	assert.Less(t, len(got), n,
		"expected the documented lost-update window to actually lose at least one write here — "+
			"if this now passes, the lost-update window Manager.Update closes is stale and should be revised")
}

// TestUpdate_AbandonedMutationDoesNotLeak proves an Update whose fn returns
// an error leaves the shared state — both the on-disk file and any Config instance
// another holder already has — completely untouched.
func TestUpdate_AbandonedMutationDoesNotLeak(t *testing.T) {
	appDir := managerTestDir(t)
	mgr := NewManager(WithAppDir(appDir))
	require.NoError(t, mgr.Update(func(d *Draft) error {
		d.DefaultAgent = "original"
		return nil
	}))

	before, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	require.Equal(t, "original", before.GetDefaultAgent())

	wantErr := errors.New("boom")
	err = mgr.Update(func(d *Draft) error {
		d.DefaultAgent = "poisoned"
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	// The Config instance obtained BEFORE the abandoned Update is untouched —
	// Update never mutates any live instance in place, only a private Config value
	// scoped to the call.
	assert.Equal(t, "original", before.GetDefaultAgent(), "a prior Config holder must never observe an abandoned mutation")

	after, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "original", after.GetDefaultAgent(), "the on-disk file must be untouched by an abandoned Update")
}

// TestReload_ReappliesPersistentOverlays proves an env/--config-set override
// survives Invalidate()+Load() onto freshly-read files: the override is not
// something only the FIRST Load() resolves, it re-applies every time exactly
// like a fresh process start would.
// runtime (ScopeMachine) is the override target here, not workspace
// (ScopeShared, this test's original target): env may not set a
// ScopeShared key (internal/config/layerscope) — a project-policy key like
// workspace must come from project/flag, never the ambient environment.
// workspace still appears below as the "freshly-added, no-override" field,
// since a plain project-file value stays legal there.
func TestReload_ReappliesPersistentOverlays(t *testing.T) {
	path := writeProjectConfig(t, "version: 6\nruntime: host\n")
	appDir := filepath.Dir(path)

	SetOverrides(confload.Overrides{
		Env: map[string]any{"RUNTIME": "container"},
	})
	t.Cleanup(ResetOverrides)

	snap, err := Load()
	require.NoError(t, err)
	require.Equal(t, "container", snap.GetRuntime(), "precondition: the override beats the file's own value")

	// Edit the file on disk directly, out from under the memo, to a DIFFERENT
	// value the override must continue to beat.
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\nruntime: host\nworkspace: worktree\n"), 0o644))

	Invalidate()
	reloaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "container", reloaded.GetRuntime(), "the override must still beat the freshly re-read file after a reload")
	assert.Equal(t, "worktree", reloaded.GetWorkspace(), "a freshly-added file value with no override must still come through")
}

// TestReload_ExistingSnapshotHoldersUnaffected proves a holder of a prior
// Config instance keeps it — Invalidate()+Load() never mutates an existing
// *Config in place, it only changes what the NEXT Load() call returns.
func TestReload_ExistingSnapshotHoldersUnaffected(t *testing.T) {
	path := writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")
	appDir := filepath.Dir(path)

	held, err := Load()
	require.NoError(t, err)
	require.Equal(t, "alpha", held.GetDefaultAgent())

	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\ndefault_agent: beta\n"), 0o644))

	Invalidate()
	reloaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "beta", reloaded.GetDefaultAgent(), "a reload must see the on-disk change")
	assert.Equal(t, "alpha", held.GetDefaultAgent(), "a Config instance obtained BEFORE the reload must be completely unaffected by it")
	assert.NotSame(t, held, reloaded, "a reload must hand back an independent Config instance, never mutate the held one")
}

// TestSnapshot_CannotBeMutatedByReaders documents (and, at the one point Go
// lets a test assert it directly, proves) that a shared *Config's
// immutability is a COMPILE error, not a runtime check: every one of Config's
// fields is
// unexported (Phase 3), so no code outside this package can even NAME
// cfg.Agents, cfg.MCP, etc., let alone assign to them — there is no runtime
// path to test here because the illegal statement never compiles. The
// closest in-repo proof is negative: internal/lm/backends,
// internal/operations, and internal/cli (85+ non-test files) read Config
// exclusively through Get<Field> accessors (see accessors.go) and the
// package builds — grep confirms zero direct field references remain
// outside internal/config. What CAN be asserted at runtime is the
// complementary half already covered by TestAccessor_ReturnedMapIsNotThe
// InternalOne (accessors_test.go): even a read-only accessor call can't be
// used to reach back into the shared instance, because every map/slice
// accessor hands back a copy.
func TestSnapshot_CannotBeMutatedByReaders(t *testing.T) {
	appDir := managerTestDir(t)
	mgr := NewManager(WithAppDir(appDir))
	require.NoError(t, mgr.Update(func(d *Draft) error {
		d.Agents = map[string]agents.Agent{"seed": {LLM: "x"}}
		return nil
	}))
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	// The only mutation surface available to a reader is through an accessor's
	// RETURNED value — and that's a copy, not the shared instance. This is the
	// runtime half of the guarantee; the field-level half
	// (cfg.agents undefined (cannot refer to unexported field agents)) is a
	// compile error that, by definition, cannot be expressed as a passing
	// runtime test — see the doc comment above.
	agentsCopy := cfg.GetConfiguredAgents()
	agentsCopy["injected"] = agents.Agent{LLM: "should-not-appear"}
	assert.NotContains(t, cfg.GetConfiguredAgents(), "injected",
		"mutating an accessor's returned copy must never reach the shared instance")
}

// TestSnapshot_CannotBeReplacedWholesale proves there is no exported,
// zero-effort way to swap out what a shared *Config points to from outside
// this package: no exported field to assign into (every Config field is
// unexported), and the only exported ways to OBTAIN a *Config — Load,
// LoadFresh, NewFixture — each return an independent value tied to a real
// source (a config.yaml, or an explicit Fixture — never an arbitrary
// in-place swap of an existing instance's contents). Two Load calls against
// different app dirs never alias.
func TestSnapshot_CannotBeReplacedWholesale(t *testing.T) {
	appDirA := managerTestDir(t)
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDirA), []byte("version: 6\ndefault_agent: a\n"), 0o644))
	a, err := Load(WithAppDir(appDirA))
	require.NoError(t, err)

	home := testsupport.Isolate(t)
	appDirB := filepath.Join(home, "other", AppDirName)
	require.NoError(t, os.MkdirAll(appDirB, 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDirB), []byte("version: 6\ndefault_agent: b\n"), 0o644))
	b, err := Load(WithAppDir(appDirB))
	require.NoError(t, err)

	assert.NotSame(t, a, b, "two Load calls against different app dirs must never return the same *Config")
	assert.Equal(t, "a", a.GetDefaultAgent())
	assert.Equal(t, "b", b.GetDefaultAgent(), "loading b must never have overwritten a's contents")
}

// TestNoArgManager_TargetsTheAmbientProject pins the property a no-argument
// Manager carries: it writes to the config.yaml the no-arg Load() resolves to
// — the ambient project discovered by walking up from cwd — not to some
// process-wide default path. This is the whole behaviour a caller that names
// no explicit target relies on.
func TestNoArgManager_TargetsTheAmbientProject(t *testing.T) {
	appDir := managerTestDir(t)
	t.Chdir(filepath.Dir(appDir))
	Invalidate()

	require.NoError(t, NewManager().Update(func(d *Draft) error {
		d.DefaultAgent = "ambient"
		return nil
	}))

	data, err := os.ReadFile(paths.ConfigPath(appDir))
	require.NoError(t, err, "a no-arg Manager must have written the ambient project's own config.yaml")
	assert.Contains(t, string(data), "ambient")
}
