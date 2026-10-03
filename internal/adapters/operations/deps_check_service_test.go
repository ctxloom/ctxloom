package operations

import (
	"context"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// depsCheckApp opens the composition over a project whose lockfile holds
// exactly the given bundle entries and whose one profile composes the given
// refs. Check reports only on entries the project composes.
func depsCheckApp(t *testing.T, bundles map[trust.BundleKey]remote.LockEntry, composes ...string) *App {
	t.Helper()
	appDir := t.TempDir()
	if bundles != nil {
		require.NoError(t, remote.NewLockfileManager(appDir).Save(&remote.Lockfile{Bundles: bundles}))
	}
	if len(composes) > 0 {
		writeLocalProfile(t, appDir, "default", "bundles:\n  - "+strings.Join(composes, "\n  - ")+"\n")
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

// TestCheckDependencies_UnparseableEntryIsRefusedNotCurrent: a lockfile
// entry whose key is not a bundle identity is refused with the lockfile, never
// read past — the result that used to be a `continue` with no diagnostic,
// which let "All items are up to date!" print with nothing checked.
// (detectUpdates' own Unchecked row for an unparseable in-memory entry is
// TestDetectUpdates_FailedChecksAreCounted.)
func TestCheckDependencies_UnparseableEntryIsRefusedNotCurrent(t *testing.T) {
	app := depsCheckApp(t, map[trust.BundleKey]remote.LockEntry{
		"::::not-a-valid-reference": {SHA: "somesha", RequestedVersion: "main"},
	})
	_, err := CheckDependencies(context.Background(), app, CheckDependenciesRequest{})
	require.ErrorIs(t, err, remote.ErrLockKeyFormRetired)
}

// TestCheckDependencies_EmptySHAEntriesAreSkippedAndCounted: an entry with
// no locked SHA was never pulled, so it has nothing to compare against; it is
// counted so the frontend can say so, never reported as current or as
// unchecked.
func TestCheckDependencies_EmptySHAEntriesAreSkippedAndCounted(t *testing.T) {
	app := depsCheckApp(t, map[trust.BundleKey]remote.LockEntry{
		"ctxloom+git://github.com/o/r//bundles/x": {SHA: "", RequestedVersion: "main"},
	}, "https://github.com/o/r@bundles/x@abc123def456")
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
