package config_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ownerTestDir isolates the environment and returns a fresh, real (OS
// filesystem) .ctxloom directory for an Owner to operate on. A real
// filesystem is required, not afero.NewMemMapFs: Update's advisory lock is
// skipped entirely for an injected test fs (no cross-process readers to
// protect), so the lock-holding tests below need real files.
func ownerTestDir(t *testing.T) string {
	t.Helper()
	home := testsupport.Isolate(t)
	appDir := filepath.Join(home, "project", config.AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	return appDir
}

// openOwner opens an Owner over the real reader pinned to appDir.
func openOwner(t *testing.T, appDir string) *config.Owner {
	t.Helper()
	src, err := configload.New(nil, nil, configload.WithAppDir(appDir))
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	return owner
}

func update(owner *config.Owner, fn func(*config.Draft) error) error {
	_, err := owner.Update(context.Background(), fn)
	return err
}

func configPathOf(t *testing.T, owner *config.Owner) string {
	t.Helper()
	p, err := owner.Current().Config.GetConfigFilePath()
	require.NoError(t, err)
	return p
}

// TestOwnerUpdate_LocksUnderStateLocksNotBesideTheConfig pins WHERE the
// sidecar lands: beside-the-file put `.ctxloom/config.yaml.lock` at the root
// of every project the moment anything wrote config, untracked — a file a
// developer is invited to `git add` by mistake. The assertion is on the file
// the transaction ACTUALLY took, observed after a real Update.
func TestOwnerUpdate_LocksUnderStateLocksNotBesideTheConfig(t *testing.T) {
	appDir := ownerTestDir(t)
	owner := openOwner(t, appDir)
	require.NoError(t, update(owner, func(d *config.Draft) error {
		d.DefaultAgent = "locked-write"
		return nil
	}))
	want, err := paths.ProjectPathFor(configPathOf(t, owner))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(appDir, paths.StateDir, paths.LocksDir), filepath.Dir(want),
		"the lock must live under state/locks")
	require.FileExists(t, want, "the update lock must be taken under state/locks")
	require.NoFileExists(t, configPathOf(t, owner)+".lock",
		"a lock beside the config file is the shape this move retired; two locations means two writers that do not exclude each other")
}

func TestOwnerUpdate_SerializesConcurrentWritersInProcess(t *testing.T) {
	appDir := ownerTestDir(t)
	owner := openOwner(t, appDir)
	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = update(owner, func(d *config.Draft) error {
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
	got := owner.Current().Config.GetConfiguredAgents()
	require.Len(t, got, n, "every concurrent writer's distinct key must survive — a lost write means Update did not serialize the read-modify-write span")
	for i := 0; i < n; i++ {
		assert.Contains(t, got, fmt.Sprintf("agent-%02d", i))
	}
	assert.Equal(t, uint64(1+n), owner.Current().Generation, "every write is a generation")
}

func TestOwnerUpdate_FailsClosedWhenLockCannotBeAcquired(t *testing.T) {
	appDir := ownerTestDir(t)
	owner := openOwner(t, appDir)
	lockPath, err := paths.ProjectPathFor(configPathOf(t, owner))
	require.NoError(t, err)
	// A directory where the lock file must be: flock cannot open it.
	require.NoError(t, os.MkdirAll(lockPath, 0o755))
	err = update(owner, func(d *config.Draft) error {
		d.DefaultAgent = "should-not-be-written"
		return nil
	})
	require.Error(t, err, "Update must fail closed rather than silently update unlocked when the lock cannot be acquired")
}

// A second writer's fresh read (taken AFTER acquiring the lock) sees the
// first writer's committed change: the whole read-modify-write span is
// serialized, not only some part of it.
func TestOwnerUpdate_HoldsFileLockAcrossReadModifyWrite(t *testing.T) {
	appDir := ownerTestDir(t)
	owner := openOwner(t, appDir)
	aEnteredCritical := make(chan struct{})
	releaseA := make(chan struct{})
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		err := update(owner, func(d *config.Draft) error {
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
		err := update(owner, func(d *config.Draft) error {
			_, ok := d.Agents["a"]
			bSawA <- ok
			d.Agents["b"] = agents.Agent{LLM: "b"}
			return nil
		})
		assert.NoError(t, err)
	}()
	select {
	case <-bDone:
		t.Fatal("writer B's Update completed while writer A still held the lock — " +
			"Update is not serializing the full read-modify-write span, only some part of it")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseA)
	<-aDone
	<-bDone
	require.True(t, <-bSawA, "writer B's fresh read (taken AFTER acquiring the lock) must see writer A's already-committed change")
	got := owner.Current().Config.GetConfiguredAgents()
	assert.Contains(t, got, "a")
	assert.Contains(t, got, "b")
}

func TestOwnerUpdate_AbandonedMutationDoesNotLeak(t *testing.T) {
	appDir := ownerTestDir(t)
	owner := openOwner(t, appDir)
	require.NoError(t, update(owner, func(d *config.Draft) error {
		d.DefaultAgent = "original"
		return nil
	}))
	before := owner.Current()
	require.Equal(t, "original", before.Config.GetDefaultAgent())
	wantErr := errors.New("boom")
	err := update(owner, func(d *config.Draft) error {
		d.DefaultAgent = "poisoned"
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	assert.Same(t, before, owner.Current(), "an abandoned Update publishes no generation")
	assert.Equal(t, "original", before.Config.GetDefaultAgent(), "a prior Config holder must never observe an abandoned mutation")
	after, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, "original", after.GetDefaultAgent(), "the on-disk file must be untouched by an abandoned Update")
}

// Mutating an accessor's returned copy must never reach the published value.
func TestOwnerCurrent_AccessorsCopy(t *testing.T) {
	appDir := ownerTestDir(t)
	owner := openOwner(t, appDir)
	require.NoError(t, update(owner, func(d *config.Draft) error {
		d.Agents = map[string]agents.Agent{"seed": {LLM: "x"}}
		return nil
	}))
	cfg := owner.Current().Config
	agentsCopy := cfg.GetConfiguredAgents()
	agentsCopy["injected"] = agents.Agent{LLM: "should-not-appear"}
	assert.NotContains(t, cfg.GetConfiguredAgents(), "injected")
}
