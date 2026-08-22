// Tests for the store's locked read-modify-write: Set and Forget must
// serialize against OTHER PROCESSES touching the same file (hooks, the MCP
// server, the CLI, the runner all share these records), not merely against
// other goroutines in this process. See store.go's lockedRMW.
//
// These tests deliberately use a REAL OS filesystem rooted at t.TempDir(),
// not afero.NewMemMapFs() like the rest of this package's tests: Store skips
// locking entirely for a non-OS-backed filesystem (see isOSBackedFs) because
// there is no other process to exclude from one, so a lost-update or
// fails-closed test built on MemMapFs would prove nothing about the real
// bug this file exists to fix.
package admission_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/admission"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// TestStore_ConcurrentSetsUnderRaceSurviveDistinctKeys proves N goroutines
// each Set-ing a DISTINCT key all survive: none is silently lost to another's
// concurrent unlocked read-modify-write, which is the store's shape before
// this fix (Load, compute kept, WriteFile, with nothing serializing the span).
//
// Run under `go test -race` (which `just test-pkg` already passes through):
// the race detector alone would not catch a LOST WRITE — two racing but
// individually race-free Load/write pairs interleave cleanly at the Go
// memory-model level, they just each overwrite the other's committed record.
// The payload assertion below (every scope present, non-empty file) is what
// actually catches that, exactly as it caught it for
// config.Manager.Update (TestUpdate_SerializesConcurrentWritersInProcess).
func TestStore_ConcurrentSetsUnderRaceSurviveDistinctKeys(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "decisions.yaml")
	s := admission.NewStore(fs, path, keyOf, testReasons(), admission.WithScope(scopeOf))

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Set(testKey{Scope: fmt.Sprintf("scope-%02d", i), Fine: "sha"}, true)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "writer %d", i)
	}

	recs, err := s.List()
	require.NoError(t, err)
	require.Len(t, recs, n,
		"every concurrent writer's distinct key must survive — a lost write means Set did not serialize the read-modify-write span")
	seen := make(map[string]bool, n)
	for _, r := range recs {
		seen[r.Key.Scope] = true
	}
	for i := 0; i < n; i++ {
		assert.True(t, seen[fmt.Sprintf("scope-%02d", i)], "scope-%02d must be present", i)
	}

	// Payload assertion with a zero-length guard: a store that silently wrote
	// nothing (this codebase's characteristic failure mode) would still pass
	// every error check above.
	body, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	require.NotEmpty(t, body, "the final store file must not be empty after concurrent writers committed")
}

// TestStore_ConcurrentDecideAsksAtMostOnceUnderLock is the Decide-shaped
// twin: Decide's Lookup happens unlocked, but the Set it falls through to
// when nothing is recorded is the same locked write Set uses directly. Two
// goroutines racing Decide for the SAME key, both non-interactively refused
// (nothing recorded, ask is nil, so neither ever writes) is not useful for
// proving locking; instead this drives Decide with an Ask so both goroutines
// answer "yes" for DISTINCT keys and both decisions must be recorded, exactly
// like the direct-Set test above but through the higher-level entry point.
func TestStore_ConcurrentDecideUnderRaceSurviveDistinctKeys(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "decisions.yaml")
	s := admission.NewStore(fs, path, keyOf, testReasons(), admission.WithScope(scopeOf))

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := testKey{Scope: fmt.Sprintf("scope-%02d", i), Fine: "sha"}
			ask := func(context.Context, testKey) (bool, bool, error) { return true, true, nil }
			_, err := s.Decide(context.Background(), k, ask)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoErrorf(t, err, "decider %d", i)
	}

	recs, err := s.List()
	require.NoError(t, err)
	require.Len(t, recs, n, "every concurrent Decide's distinct key must survive")
}

// TestStore_LockAcquisitionFailureFailsClosedFileUntouched pins the other
// half of the locked-RMW shape: a lock the store cannot ACQUIRE must refuse
// the write outright, never fall back to writing unlocked (which would
// silently discard the serialization this whole change exists to provide,
// on every subsequent call). Forced deterministically, no contention
// involved, by making the lock file's path a pre-existing directory so
// os.OpenFile(O_CREATE|O_RDWR) on it fails outright — the identical shape
// TestUpdate_FailsClosedWhenLockCannotBeAcquired uses for
// config.Manager.Update.
func TestStore_LockAcquisitionFailureFailsClosedFileUntouched(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "decisions.yaml")
	s := admission.NewStore(fs, path, keyOf, testReasons(), admission.WithScope(scopeOf))

	// Seed one committed record so the assertion below proves an EXISTING
	// file survives untouched, not merely that a file was never created.
	_, err := s.Set(testKey{Scope: "bin", Fine: "sha-old"}, true)
	require.NoError(t, err)
	before, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	require.NotEmpty(t, before)

	lockPath := paths.PathFor(path) // the store's default (no WithLockPathFor override)
	// The seed Set above already acquired-and-released this exact lock file,
	// which left it on disk as a FILE (filelock never removes its lock
	// files, only unlocks them) — clear it before replacing it with a
	// directory, or MkdirAll fails trying to turn a file into one.
	require.NoError(t, os.RemoveAll(lockPath))
	require.NoError(t, os.MkdirAll(lockPath, 0o755))

	_, err = s.Set(testKey{Scope: "bin", Fine: "sha-new"}, true)
	require.Error(t, err, "Set must fail closed rather than silently writing unlocked when the lock cannot be acquired")

	after, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.Equal(t, before, after, "a failed lock acquisition must leave the records file byte-for-byte untouched")
}

// TestStore_Write_UsesDurableWrite pins the ruled site (taskloom
// unbounded-bacon): Store.write must pass iox.Durable() so a crash cannot
// silently revert an admission decision — the store's whole purpose, per
// its package doc's ssh known_hosts comparison, is a record that once made
// is never silently un-made. This test belongs in this file, not
// admission_test.go's MemMapFs-backed tests, for the same reason those
// exist: Durable() is a no-op on a non-OS-backed filesystem, so only a real
// one gives the seam anything to fire on.
func TestStore_Write_UsesDurableWrite(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "decisions.yaml")
	s := admission.NewStore(fs, path, keyOf, testReasons(), admission.WithScope(scopeOf))

	var synced []string
	restore := iox.SetSyncDirForTesting(func(d string) error {
		synced = append(synced, d)
		return nil
	})
	defer restore()

	_, err := s.Set(testKey{Scope: "bin", Fine: "sha"}, true)
	require.NoError(t, err)
	assert.NotEmpty(t, synced, "Store.write must pass iox.Durable(): a reverted admission decision after a crash re-opens a door a human closed")
}
