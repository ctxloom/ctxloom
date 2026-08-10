package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

const crIdentity = "https://github.com/o/r@bundles/x"

// countingFactory returns a FetcherFactory yielding mock and a pointer to its
// invocation count, so a test can prove a path resolved WITHOUT touching a repo.
func countingFactory(mock remote.Fetcher) (remote.FetcherFactory, *int) {
	calls := 0
	f := func(string, remote.AuthConfig) (remote.Fetcher, error) {
		calls++
		return mock, nil
	}
	return f, &calls
}

func activeLock(entry remote.LockEntry) *remote.Lockfile {
	lf := &remote.Lockfile{
		Bundles: map[string]remote.LockEntry{},
	}
	lf.AddEntry(remote.ItemTypeBundle, crIdentity, entry)
	return lf
}

func mustRef(t *testing.T, expr string) *remote.Reference {
	t.Helper()
	s := crIdentity
	if expr != "" {
		s += "@" + expr
	}
	ref, err := remote.ParseReference(s)
	require.NoError(t, err)
	return ref
}

func TestConstraintResolver_BareCommitIsVerbatimNoClone(t *testing.T) {
	factory, calls := countingFactory(remote.NewMockFetcher())
	resolve := newConstraintResolver(context.Background(), nil, factory, remote.AuthConfig{}, false)

	sha, version, _, ok := resolve(mustRef(t, "abc123def456"))
	require.True(t, ok)
	assert.Equal(t, "abc123def456", sha)
	assert.Empty(t, version)
	assert.Zero(t, *calls, "a bare commit must resolve without building a fetcher")
}

func TestConstraintResolver_CarryForwardUnchangedConstraint(t *testing.T) {
	factory, calls := countingFactory(remote.NewMockFetcher())
	active := activeLock(remote.LockEntry{SHA: "lockedsha", RequestedVersion: "^1.2", Version: "v1.2.5"})
	resolve := newConstraintResolver(context.Background(), active, factory, remote.AuthConfig{}, false)

	sha, version, _, ok := resolve(mustRef(t, "^1.2"))
	require.True(t, ok)
	assert.Equal(t, "lockedsha", sha, "unchanged constraint keeps the locked SHA")
	assert.Equal(t, "v1.2.5", version)
	assert.Zero(t, *calls, "carry-forward must not hit the repo")
}

func TestConstraintResolver_HoldWinsOverChangedConstraint(t *testing.T) {
	factory, calls := countingFactory(remote.NewMockFetcher())
	active := activeLock(remote.LockEntry{SHA: "heldsha", RequestedVersion: "^1.0", Held: true})
	resolve := newConstraintResolver(context.Background(), active, factory, remote.AuthConfig{}, false)

	// Constraint changed (^2.0), but the entry is held → stays frozen.
	sha, _, _, ok := resolve(mustRef(t, "^2.0"))
	require.True(t, ok)
	assert.Equal(t, "heldsha", sha)
	assert.Zero(t, *calls, "a held entry is never re-resolved")
}

func TestConstraintResolver_FreshBranchResolution(t *testing.T) {
	mock := remote.NewMockFetcher()
	mock.Refs = map[string]string{"main": "mainsha"}
	factory, calls := countingFactory(mock)
	resolve := newConstraintResolver(context.Background(), nil, factory, remote.AuthConfig{}, false)

	sha, _, _, ok := resolve(mustRef(t, "main"))
	require.True(t, ok)
	assert.Equal(t, "mainsha", sha)
	assert.Equal(t, 1, *calls, "a branch constraint with no lock resolves against the repo")
}

func TestConstraintResolver_FallsBackToLockOnFailure(t *testing.T) {
	mock := remote.NewMockFetcher()
	mock.ResolveRefErr = errors.New("offline")
	factory, _ := countingFactory(mock)
	active := activeLock(remote.LockEntry{SHA: "oldsha", RequestedVersion: "release"})
	resolve := newConstraintResolver(context.Background(), active, factory, remote.AuthConfig{}, false)

	// Changed constraint forces a fresh resolve, which fails → fall back to lock.
	sha, _, _, ok := resolve(mustRef(t, "feature-x"))
	require.True(t, ok)
	assert.Equal(t, "oldsha", sha, "a resolution failure keeps the last known SHA, never empty")
}

func TestConstraintResolver_SkipsWhenUnresolvableAndUnlocked(t *testing.T) {
	mock := remote.NewMockFetcher()
	mock.ResolveRefErr = errors.New("offline")
	factory, _ := countingFactory(mock)
	resolve := newConstraintResolver(context.Background(), nil, factory, remote.AuthConfig{}, false)

	_, _, _, ok := resolve(mustRef(t, "feature-x"))
	assert.False(t, ok, "no resolution and no prior lock → skip, not an empty pin")
}

func TestConstraintResolver_UpgradeReResolvesButHoldStays(t *testing.T) {
	// Upgrade mode (reResolve=true): an unchanged-constraint entry is NOT carried
	// forward — it re-resolves to the newest commit — but a held entry stays put.
	mock := remote.NewMockFetcher()
	mock.Refs = map[string]string{"main": "newmainsha"}
	factory, calls := countingFactory(mock)

	t.Run("unheld re-resolves past the locked SHA", func(t *testing.T) {
		active := activeLock(remote.LockEntry{SHA: "oldmainsha", RequestedVersion: "main"})
		resolve := newConstraintResolver(context.Background(), active, factory, remote.AuthConfig{}, true)
		sha, _, _, ok := resolve(mustRef(t, "main"))
		require.True(t, ok)
		assert.Equal(t, "newmainsha", sha, "upgrade advances an unheld branch past its locked SHA")
		assert.Equal(t, 1, *calls)
	})

	t.Run("held stays frozen even under upgrade", func(t *testing.T) {
		factory2, calls2 := countingFactory(mock)
		active := activeLock(remote.LockEntry{SHA: "frozensha", RequestedVersion: "main", Held: true})
		resolve := newConstraintResolver(context.Background(), active, factory2, remote.AuthConfig{}, true)
		sha, _, _, ok := resolve(mustRef(t, "main"))
		require.True(t, ok)
		assert.Equal(t, "frozensha", sha, "a held entry never moves, even in upgrade mode")
		assert.Zero(t, *calls2)
	})
}

func TestConstraintResolver_MemoizesPerConstraint(t *testing.T) {
	mock := remote.NewMockFetcher()
	mock.Refs = map[string]string{"main": "mainsha"}
	factory, calls := countingFactory(mock)
	resolve := newConstraintResolver(context.Background(), nil, factory, remote.AuthConfig{}, false)

	resolve(mustRef(t, "main"))
	resolve(mustRef(t, "main"))
	assert.Equal(t, 1, *calls, "the same (identity, constraint) resolves once per flatten")
}
