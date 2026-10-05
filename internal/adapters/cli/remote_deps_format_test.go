package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The `remote` and `deps` verbs below carry a --format json payload whose
// SHAPE the owner signed off as a public consumer contract. Each test drives
// the verb the way a pipeline does — stdout not a terminal, --format never
// typed — and pins the decoded payload's exact key set, so a renamed,
// dropped or added key fails here rather than in somebody's jq.

// pipedCmd is a command as a piped caller presents it: --format registered
// but unset, stdout not a terminal, so cliemit.Resolve derives json.
func pipedCmd(t *testing.T) (cmd *cobra.Command, stdout, stderr *bytes.Buffer) {
	t.Helper()
	t.Cleanup(cliemit.OverrideTerminal(false))
	cmd = &cobra.Command{}
	cmd.Flags().String("format", formatText, "")
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetContext(context.Background())
	return cmd, stdout, stderr
}

// decodeObject unmarshals stdout as ONE JSON object — nothing else may share
// stdout with the payload — and returns it.
func decodeObject(t *testing.T, stdout *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), "stdout must be exactly one JSON payload:\n%s", stdout.String())
	return got
}

// assertKeys pins an object's key set to exactly want.
func assertKeys(t *testing.T, obj any, want ...string) {
	t.Helper()
	m, ok := obj.(map[string]any)
	require.True(t, ok, "expected a JSON object, got %T", obj)
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

// stub replaces a package-level seam for one test.
func stub[T any](t *testing.T, seam *T, fake T) {
	t.Helper()
	orig := *seam
	*seam = fake
	t.Cleanup(func() { *seam = orig })
}

func TestRemoteCreate_PipedEmitsTheApprovedShape(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &addRemote, func(_ context.Context, _ *config.Config, req operations.AddRemoteRequest) (*operations.AddRemoteResult, error) {
		return &operations.AddRemoteResult{Status: "added", Name: req.Name, URL: "https://github.com/corp/ctxloom",
			Warning: "repository does not have a ctxloom/ directory structure"}, nil
	})
	cmd, stdout, _ := pipedCmd(t)

	require.NoError(t, runRemoteCreate(cmd, []string{"corp", "corp/ctxloom"}))

	got := decodeObject(t, stdout)
	assertKeys(t, got, "status", "name", "url", "warning")
	assert.Equal(t, "added", got["status"])
	assert.Equal(t, "corp", got["name"])
}

func TestRemoteShow_PipedEmitsTheApprovedShape(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &browseRemote, func(context.Context, *config.Config, operations.BrowseRemoteRequest) (*operations.BrowseRemoteResult, error) {
		return &operations.BrowseRemoteResult{Remote: "corp", URL: "https://github.com/corp/ctxloom", Count: 2,
			Items: []operations.BrowseItemEntry{
				{Name: "zeta", Type: "bundle", Path: "bundles/zeta", IsDir: true, PullRef: "corp/zeta"},
				{Name: "alpha", Type: "bundle", Path: "bundles/alpha", PullRef: "corp/alpha"},
			}}, nil
	})
	cmd, stdout, _ := pipedCmd(t)

	require.NoError(t, runRemoteBrowse(cmd, []string{"corp"}))

	got := decodeObject(t, stdout)
	assertKeys(t, got, "remote", "url", "items", "count", "warnings")
	assert.Equal(t, []any{}, got["warnings"], "an empty list is [], present")
	items := got["items"].([]any)
	require.Len(t, items, 2)
	assertKeys(t, items[0], "name", "type", "path", "is_dir", "pull_ref")
	assert.Equal(t, "bundles/alpha", items[0].(map[string]any)["path"], "items are sorted by path")
	assert.Equal(t, false, items[0].(map[string]any)["is_dir"])
}

func TestRemoteShow_PipedEmptyRemoteIsAnEmptyList(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &browseRemote, func(context.Context, *config.Config, operations.BrowseRemoteRequest) (*operations.BrowseRemoteResult, error) {
		return &operations.BrowseRemoteResult{Remote: "corp", URL: "u"}, nil
	})
	cmd, stdout, _ := pipedCmd(t)

	require.NoError(t, runRemoteBrowse(cmd, []string{"corp"}))

	got := decodeObject(t, stdout)
	assert.EqualValues(t, 0, got["count"])
	assert.Equal(t, []any{}, got["items"])
}

// A partial browse returned warnings that text mode used to drop on the
// floor: the listing read as complete when part of the remote was never read.
func TestRemoteShow_TextModePrintsPartialBrowseWarningsOnStderr(t *testing.T) {
	testsupport.Isolate(t)
	const partial = "bundles/broken: could not read directory"
	stub(t, &browseRemote, func(context.Context, *config.Config, operations.BrowseRemoteRequest) (*operations.BrowseRemoteResult, error) {
		return &operations.BrowseRemoteResult{Remote: "corp", URL: "u", Count: 1, Warnings: []string{partial},
			Items: []operations.BrowseItemEntry{{Name: "a", Type: "bundle", Path: "bundles/a", PullRef: "corp/a"}}}, nil
	})
	cmd, stdout, stderr := pipedCmd(t)
	require.NoError(t, cmd.Flags().Set("format", formatText))

	require.NoError(t, runRemoteBrowse(cmd, []string{"corp"}))

	assert.Contains(t, stderr.String(), partial)
	assert.NotContains(t, stdout.String(), partial, "a warning is a diagnostic, not part of the listing")
}

func TestRemoteDefault_PipedEmitsTheApprovedShape(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &setDefaultRemote, func(_ context.Context, _ *config.Config, req operations.DefaultRemoteRequest) (*operations.DefaultRemoteResult, error) {
		if req.Name == "" {
			return &operations.DefaultRemoteResult{Status: "cleared"}, nil
		}
		return &operations.DefaultRemoteResult{Status: "set", Name: req.Name}, nil
	})
	stub(t, &remoteDefaultClear, false)

	cmd, stdout, _ := pipedCmd(t)
	require.NoError(t, runRemoteDefault(cmd, []string{"corp"}))
	got := decodeObject(t, stdout)
	assertKeys(t, got, "status", "name")
	assert.Equal(t, "set", got["status"])

	remoteDefaultClear = true
	cmd, stdout, _ = pipedCmd(t)
	require.NoError(t, runRemoteDefault(cmd, nil))
	got = decodeObject(t, stdout)
	assertKeys(t, got, "status")
	assert.Equal(t, "cleared", got["status"])
}

func TestRemoteDiscover_PipedEmitsTheApprovedShape(t *testing.T) {
	cmd, stdout, stderr := pipedCmd(t)

	require.NoError(t, runRemoteDiscover(cmd, nil, discoverTestConfig, discoverOneRepo()))

	got := decodeObject(t, stdout)
	assertKeys(t, got, "repositories", "count", "errors")
	assert.Equal(t, []any{}, got["errors"])
	repos := got["repositories"].([]any)
	require.Len(t, repos, 1)
	assertKeys(t, repos[0], "owner", "name", "description", "stars", "url", "forge", "add_command")
	assert.Equal(t, "ctxloom remote create ctxloom alice/ctxloom", repos[0].(map[string]any)["add_command"],
		"the command it names must exist")
	assert.Contains(t, stderr.String(), "Searching repositories... found 1", "progress belongs on stderr")
	assert.NotContains(t, stdout.String(), "Add remote?", "no prompt in a machine payload")
}

func TestDepsPull_PipedEmitsTheApprovedShapeThenFails(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &depsPullLock, true)
	stub(t, &syncDependencies, func(context.Context, *operations.App, operations.SyncDependenciesRequest) (*operations.SyncDependenciesResult, error) {
		return &operations.SyncDependenciesResult{
			Status: "completed_with_errors", Total: 2, Installed: 1, Errors: 1,
			Synced: []operations.SyncItem{{Reference: "corp/a", Type: "bundle", Status: "installed", LocalPath: "/p/a"}},
			Failed: []operations.SyncItem{{Reference: "corp/b", Type: "bundle", Status: "failed", Error: "fetch failed"}},
		}, nil
	})
	stub(t, &reconcileInstalledOp, func(context.Context, *config.Config) (operations.ReconcileResult, error) {
		return operations.ReconcileResult{Plan: operations.ReconcilePlan{
			Gone:        []trust.BundleKey{"corp/old"},
			Unreachable: []operations.UncheckedRemote{{URL: "https://x", Reason: "timeout", Refs: []trust.BundleKey{"corp/c"}}},
		}}, nil
	})
	cmd, stdout, stderr := pipedCmd(t)

	err := runDepsPull(cmd, nil)
	require.Error(t, err, "a failed item still fails the pull — after the payload")

	got := decodeObject(t, stdout)
	assertKeys(t, got, "status", "total", "installed", "updated", "errors", "synced", "skipped", "retracted",
		"failed", "removed", "incomplete", "unreachable", "message", "reconcile")
	assert.Equal(t, "completed_with_errors", got["status"])
	assert.Equal(t, []any{}, got["skipped"])
	assertKeys(t, got["synced"].([]any)[0], "reference", "type", "status", "local_path")
	failed := got["failed"].([]any)
	require.Len(t, failed, 1)
	assertKeys(t, failed[0], "reference", "type", "status", "error", "fix")
	assert.NotEmpty(t, failed[0].(map[string]any)["fix"])
	reconcile := got["reconcile"]
	assertKeys(t, reconcile, "gone", "unreachable")
	assert.Equal(t, []any{"corp/old"}, reconcile.(map[string]any)["gone"])
	assertKeys(t, reconcile.(map[string]any)["unreachable"].([]any)[0], "url", "reason", "refs")
	assert.Contains(t, stderr.String(), "Pulling dependencies...", "progress belongs on stderr")
}

// --lock=false leaves the lockfile alone, so no reconcile ran and the
// payload carries none — absent, not an empty plan claiming a clean check.
func TestDepsPull_PipedWithoutLockOmitsReconcile(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &depsPullLock, false)
	stub(t, &syncDependencies, func(context.Context, *operations.App, operations.SyncDependenciesRequest) (*operations.SyncDependenciesResult, error) {
		return &operations.SyncDependenciesResult{Status: "empty", Message: "No remote dependencies"}, nil
	})
	stub(t, &reconcileInstalledOp, func(context.Context, *config.Config) (operations.ReconcileResult, error) {
		t.Fatal("reconcile must not run under --lock=false")
		return operations.ReconcileResult{}, nil
	})
	cmd, stdout, _ := pipedCmd(t)

	require.NoError(t, runDepsPull(cmd, nil))

	got := decodeObject(t, stdout)
	assert.NotContains(t, got, "reconcile")
	assert.Equal(t, "empty", got["status"])
	assert.Equal(t, []any{}, got["synced"])
}

func TestDepsCheck_PipedEmitsTheApprovedShape(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &checkDependencies, func(context.Context, *operations.App, operations.CheckDependenciesRequest) (operations.CheckDependenciesResult, error) {
		return operations.CheckDependenciesResult{
			Entries: 3,
			Updates: []operations.DependencyUpdate{{Type: remote.ItemTypeBundle, Ref: "corp/a",
				CurrentSHA: "1111111111111111111111111111111111111111", LatestSHA: "2222222222222222222222222222222222222222",
				RequestedVersion: "^1.2", Kind: remote.SelectorVersion, Version: "v1.3.0"}},
			Unchecked: []operations.UncheckedDependency{
				{Ref: "corp/b", URL: "https://x", Reason: operations.UncheckedNotRefreshed, Err: errors.New("dial tcp")},
				{Ref: "corp/c", Reason: operations.UncheckedNoRepositoryURL},
			},
			Refresh: []operations.RefreshFailure{{URL: "https://x", Err: errors.New("dial tcp")}},
		}, nil
	})
	cmd, stdout, stderr := pipedCmd(t)

	require.NoError(t, runDepsCheck(cmd, nil))

	got := decodeObject(t, stdout)
	assertKeys(t, got, "entries", "updates", "unchecked", "refresh_failures", "skipped_empty",
		"missing_default_profiles", "missing_defaults_error")
	assert.Equal(t, []any{}, got["missing_default_profiles"], "always present")
	update := got["updates"].([]any)[0]
	assertKeys(t, update, "ref", "type", "current_sha", "latest_sha", "selector", "requested", "resolved")
	assert.Equal(t, "2222222222222222222222222222222222222222", update.(map[string]any)["latest_sha"], "full SHA, not short")
	assert.Equal(t, "version", update.(map[string]any)["selector"])
	unchecked := got["unchecked"].([]any)
	assertKeys(t, unchecked[0], "ref", "url", "constraint", "reason", "error")
	assert.Equal(t, "not_refreshed", unchecked[0].(map[string]any)["reason"])
	assert.Equal(t, "no_repository_url", unchecked[1].(map[string]any)["reason"])
	assertKeys(t, got["refresh_failures"].([]any)[0], "url", "error")
	assert.Contains(t, stderr.String(), "Checking 3 items for updates...", "progress belongs on stderr")
}

func TestDepsCheck_UncheckedReasonVocabularyIsTotal(t *testing.T) {
	for reason, want := range map[operations.UncheckedReason]string{
		operations.UncheckedUnparseable:     "unparseable",
		operations.UncheckedNoRepositoryURL: "no_repository_url",
		operations.UncheckedUnreachable:     "unreachable",
		operations.UncheckedUnresolvable:    "unresolvable",
		operations.UncheckedNotRefreshed:    "not_refreshed",
	} {
		assert.Equal(t, want, uncheckedReasonName(reason))
	}
}

func TestDepsCheckRef_PipedEmitsTheApprovedShape(t *testing.T) {
	testsupport.Isolate(t)
	stub(t, &checkDependencies, func(_ context.Context, _ *operations.App, req operations.CheckDependenciesRequest) (operations.CheckDependenciesResult, error) {
		return operations.CheckDependenciesResult{Single: &operations.DependencyStatus{Ref: req.Ref,
			CurrentSHA: "1111111111111111111111111111111111111111", LatestSHA: "1111111111111111111111111111111111111111"}}, nil
	})
	cmd, stdout, _ := pipedCmd(t)

	require.NoError(t, runDepsCheck(cmd, []string{"corp/a"}))

	got := decodeObject(t, stdout)
	assertKeys(t, got, "ref", "in_lockfile", "current_sha", "latest_sha", "up_to_date", "refresh_failures")
	assert.Equal(t, true, got["in_lockfile"])
	assert.Equal(t, true, got["up_to_date"])
	assert.Equal(t, []any{}, got["refresh_failures"])
}

func TestDepsUpgrade_PipedEmitsTheApprovedShapeThenExitsRefused(t *testing.T) {
	stub(t, &upgradeDependencies, func(context.Context, *config.Config, []string) (operations.UpgradeResult, error) {
		return operations.UpgradeResult{Advanced: 1, Refused: []operations.RefusedAdvance{{Identity: "corp/a",
			KeptSHA: "1111", ProposedSHA: "2222", Detail: "bad sig", Cause: operations.RefusalSignature}}}, nil
	})
	cmd, stdout, stderr := pipedCmd(t)

	err := runDepsUpgrade(cmd, discoverTestConfig)

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitCodeRefused, exitErr.Code, "a refusal exits 2 — after the payload")
	got := decodeObject(t, stdout)
	assertKeys(t, got, "advanced", "incomplete", "nothing_declared", "refused", "removed")
	assert.Equal(t, []any{}, got["removed"])
	assertKeys(t, got["refused"].([]any)[0], "identity", "kept_sha", "proposed_sha", "detail", "cause")
	assert.Equal(t, "signature", got["refused"].([]any)[0].(map[string]any)["cause"])
	assert.NotContains(t, stdout.String(), "REFUSED", "refusal prose is the text rendering only")
	assert.Contains(t, stderr.String(), "Resolving latest commits", "progress belongs on stderr")
}
