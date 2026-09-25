package operations

import (
	"context"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// depsCheckApp opens the composition over a project whose lockfile holds
// exactly the given bundle entries.
func depsCheckApp(t *testing.T, bundles map[trust.BundleKey]remote.LockEntry) *App {
	t.Helper()
	appDir := t.TempDir()
	if bundles != nil {
		require.NoError(t, remote.NewLockfileManager(appDir).Save(&remote.Lockfile{Bundles: bundles}))
	}
	return fixtureApp(t, config.NewFixture(config.Fixture{AppPaths: []string{appDir}}))
}

// TestCheckDependencies_NothingInstalled_ReportsNoEntries: an empty lockfile
// is "nothing to check", stated as a count the frontend can render, not as a
// printed line.
func TestCheckDependencies_NothingInstalled_ReportsNoEntries(t *testing.T) {
	app := depsCheckApp(t, nil)
	res, err := CheckDependencies(context.Background(), app, CheckDependenciesRequest{})
	require.NoError(t, err)
	assert.Zero(t, res.Entries)
	assert.Empty(t, res.Updates)
	assert.Empty(t, res.Unchecked)
	assert.Nil(t, res.Single)
}

// TestCheckDependencies_UnparseableEntryIsUncheckedNotCurrent: a lockfile
// entry this build cannot parse is a typed Unchecked row — the result that
// used to be a `continue` with no diagnostic, which let "All items are up to
// date!" print with nothing checked.
func TestCheckDependencies_UnparseableEntryIsUncheckedNotCurrent(t *testing.T) {
	app := depsCheckApp(t, map[trust.BundleKey]remote.LockEntry{
		"::::not-a-valid-reference": {SHA: "somesha", RequestedVersion: "main"},
	})
	res, err := CheckDependencies(context.Background(), app, CheckDependenciesRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Entries)
	assert.Empty(t, res.Updates)
	assert.Zero(t, res.SkippedEmpty)
	require.Len(t, res.Unchecked, 1)
	assert.Equal(t, "::::not-a-valid-reference", res.Unchecked[0].Ref)
	assert.Equal(t, UncheckedUnparseable, res.Unchecked[0].Reason)
	assert.Error(t, res.Unchecked[0].Err)
}

// TestCheckDependencies_EmptySHAEntriesAreSkippedAndCounted: an entry with
// no locked SHA was never pulled, so it has nothing to compare against; it is
// counted so the frontend can say so, never reported as current or as
// unchecked.
func TestCheckDependencies_EmptySHAEntriesAreSkippedAndCounted(t *testing.T) {
	app := depsCheckApp(t, map[trust.BundleKey]remote.LockEntry{
		"https://github.com/o/r@bundles/x": {SHA: "", RequestedVersion: "main"},
	})
	res, err := CheckDependencies(context.Background(), app, CheckDependenciesRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Entries)
	assert.Equal(t, 1, res.SkippedEmpty)
	assert.Empty(t, res.Unchecked)
	assert.Empty(t, res.Updates)
}

// TestCheckDependencies_SingleRef_RejectsAReferenceWithNoRepositoryURL: the
// one rejection point for a single-ref check is the service, with the same
// message for the same input whichever frontend asks.
func TestCheckDependencies_SingleRef_RejectsAReferenceWithNoRepositoryURL(t *testing.T) {
	app := depsCheckApp(t, nil)
	_, err := CheckDependencies(context.Background(), app, CheckDependenciesRequest{Ref: "ctxloom:local@bundles/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reference has no repository URL")
}

// TestReconcileInstalled_EmptyClosureRemovesNothing: a project with nothing
// installed has nothing upstream could have withdrawn — no plan, no probe.
func TestReconcileInstalled_EmptyClosureRemovesNothing(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	res, err := ReconcileInstalled(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, res.Plan.Gone)
	assert.Empty(t, res.Plan.Unreachable)
	assert.Empty(t, res.Warnings)
}
