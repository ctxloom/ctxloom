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
// filelock is skipped entirely for an injected test fs (Save's own doc
// explains why — no cross-process readers to protect), so the lock-holding
// tests below need real files.
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
				d.Agents[fmt.Sprintf("agent-%02d", i)] = agents.Agent{Engine: fmt.Sprintf("engine-%02d", i)}
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

// TestUpdate_HoldsFileLockAcrossReadModifyWrite is the lost-update guard:
// it proves the lock spans the WHOLE transaction, not just the final write.
// Writer A is parked mid-transaction (inside fn, lock held); writer B's
// Update call is started concurrently and must be UNABLE to complete while A
// holds the lock — if Update only locked around the write (like Save() alone
// does), B could read stale pre-A state and complete before A ever commits.
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
			d.Agents["a"] = agents.Agent{Engine: "a"}
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
			d.Agents["b"] = agents.Agent{Engine: "b"}
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

// TestConfig_Save_AloneLosesConcurrentWrites is the empirical negative
// control for TestUpdate_SerializesConcurrentWritersInProcess above: it
// reproduces the EXACT shape every write call site used before migrating
// onto Manager.Update — LoadFresh, mutate the in-memory copy, Save() — and
// proves it silently loses writes, which is precisely what Save's own doc
// comment says it does not protect against ("two callers that each Load(),
// mutate their own in-memory copy, then Save() can still silently discard
// one another's change"). n goroutines each capture a fresh snapshot BEFORE
// any of them takes Save's advisory lock, so Save's read-merge-write only
// ever merges each goroutine's own single added key onto whatever was on
// disk at THAT goroutine's read time — not onto whatever the lock's prior
// holder just committed. The advisory lock alone (fix 4df0cbd2) only ensures
// the WRITE itself is atomic and serialized; it was never sufficient by
// itself, which is exactly why Manager.Update re-reads fresh AFTER acquiring
// the lock instead of merging a pre-lock snapshot.
func TestConfig_Save_AloneLosesConcurrentWrites(t *testing.T) {
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
			cfg.agents[fmt.Sprintf("agent-%02d", i)] = agents.Agent{Engine: fmt.Sprintf("engine-%02d", i)}
			errs[i] = cfg.Save()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "writer %d", i)
	}

	final, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	got := final.GetConfiguredAgents()

	t.Logf("bare LoadFresh-mutate-Save: %d of %d concurrent writes survived (%d lost)", len(got), n, n-len(got))
	assert.Less(t, len(got), n,
		"expected the documented lost-update window to actually lose at least one write here — "+
			"if this now passes, Save's own doc comment about the window it does not close is stale and should be revised")
}

// TestUpdate_AbandonedMutationDoesNotLeak proves an Update whose fn returns
// an error leaves the shared state — both the on-disk file and any Snapshot
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

	// The Snapshot obtained BEFORE the abandoned Update is untouched — Update
	// never mutates any live *Snapshot in place, only a private Config value
	// scoped to the call.
	assert.Equal(t, "original", before.GetDefaultAgent(), "a prior Snapshot holder must never observe an abandoned mutation")

	after, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "original", after.GetDefaultAgent(), "the on-disk file must be untouched by an abandoned Update")
}

// TestReload_ReappliesPersistentOverlays proves an env/--config-set override
// survives Reload onto freshly-read files: the override is not something
// only the FIRST Current() resolves, it re-applies every time exactly like a
// fresh process start would.
func TestReload_ReappliesPersistentOverlays(t *testing.T) {
	path := writeProjectConfig(t, "version: 6\nworkspace: none\n")
	appDir := filepath.Dir(path)

	SetOverrides(confload.Overrides{
		Env: map[string]any{"WORKSPACE": "worktree"},
	})
	t.Cleanup(ResetOverrides)

	snap, err := Current()
	require.NoError(t, err)
	require.Equal(t, "worktree", snap.GetWorkspace(), "precondition: the override beats the file's own value")

	// Edit the file on disk directly, out from under the memo, to a DIFFERENT
	// value the override must continue to beat.
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\nworkspace: worktree\nruntime: container\n"), 0o644))

	reloaded, err := Reload()
	require.NoError(t, err)
	assert.Equal(t, "worktree", reloaded.GetWorkspace(), "the override must still beat the freshly re-read file after Reload")
	assert.Equal(t, "container", reloaded.GetRuntime(), "a freshly-added file value with no override must still come through")
}

// TestReload_ExistingSnapshotHoldersUnaffected proves a holder of a prior
// Snapshot keeps it — Reload never mutates a *Snapshot in place, it only
// changes what the NEXT Current() call returns.
func TestReload_ExistingSnapshotHoldersUnaffected(t *testing.T) {
	path := writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")
	appDir := filepath.Dir(path)

	held, err := Current()
	require.NoError(t, err)
	require.Equal(t, "alpha", held.GetDefaultAgent())

	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\ndefault_agent: beta\n"), 0o644))

	reloaded, err := Reload()
	require.NoError(t, err)
	assert.Equal(t, "beta", reloaded.GetDefaultAgent(), "Reload must see the on-disk change")
	assert.Equal(t, "alpha", held.GetDefaultAgent(), "a Snapshot obtained BEFORE Reload must be completely unaffected by it")
	assert.NotSame(t, held, reloaded, "Reload must hand back an independent Snapshot, never mutate the held one")
}

// TestSnapshot_CannotBeMutatedByReaders documents (and, at the one point Go
// lets a test assert it directly, proves) that Snapshot's immutability is a
// COMPILE error, not a runtime check: every one of Config's fields is
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
		d.Agents = map[string]agents.Agent{"seed": {Engine: "x"}}
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
	agentsCopy["injected"] = agents.Agent{Engine: "should-not-appear"}
	assert.NotContains(t, cfg.GetConfiguredAgents(), "injected",
		"mutating an accessor's returned copy must never reach the shared Snapshot")
}

// TestSnapshot_CannotBeReplacedWholesale proves there is no exported,
// zero-effort way to swap out what a *Snapshot points to from outside this
// package: no exported field to assign into (Snapshot IS Config, and every
// Config field is unexported), and the only exported ways to OBTAIN a
// *Snapshot — Load, LoadFresh, Current, Reload, NewFixture — each return an
// independent value tied to a real source (a config.yaml, or an explicit
// Fixture — never an arbitrary in-place swap of an existing *Snapshot's
// contents). Two Load calls against different app dirs never alias.
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

	assert.NotSame(t, a, b, "two Load calls against different app dirs must never return the same *Snapshot")
	assert.Equal(t, "a", a.GetDefaultAgent())
	assert.Equal(t, "b", b.GetDefaultAgent(), "loading b must never have overwritten a's contents")
}
