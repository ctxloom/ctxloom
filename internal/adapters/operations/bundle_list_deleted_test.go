package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

func mustDeletedRef(t *testing.T, ref string) *remote.Reference {
	t.Helper()
	r, err := remote.ParseReference(ref)
	require.NoError(t, err)
	return r
}

// TestDeletedDependencies_OnlyWhatTheProjectDependsOn: `bundle list` lists
// what is INSTALLED. A remote's history also holds every bundle it ever
// dropped — most of which this project never pulled — so the removed-upstream
// marker is for a lockfile dependency alone. A fresh project must not open its
// first listing with bundles it never had, flagged as vanished.
func TestDeletedDependencies_OnlyWhatTheProjectDependsOn(t *testing.T) {
	const (
		dependedOn = "ctxloom+git://github.com/acme/content//bundles/gone"
		neverHad   = "ctxloom+git://github.com/acme/content//bundles/old-layout"
	)
	gone := mustDeletedRef(t, dependedOn)
	goneKey, err := gone.LockKey()
	require.NoError(t, err)
	lock := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{goneKey: {}}}

	got := deletedDependencies([]*remote.Reference{gone, mustDeletedRef(t, neverHad)}, lock)

	assert.Equal(t, []trust.BundleKey{goneKey}, got)
}

// TestDeletedDependencies_NoLockfileFlagsNothing: with no readable lockfile
// there is nothing the project is known to depend on (the unreadable case is
// already warned where the lockfile is read).
func TestDeletedDependencies_NoLockfileFlagsNothing(t *testing.T) {
	gone := mustDeletedRef(t, "ctxloom+git://github.com/acme/content//bundles/gone")
	assert.Empty(t, deletedDependencies([]*remote.Reference{gone}, nil))
}
