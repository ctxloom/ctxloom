package operations

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// U084-F13 (half one): sortContentInfos took an unvalidated sortBy and had no
// default branch, so any value other than "name"/"source" returned the input in
// whatever order the loader produced it — silently, with no warning. That is
// ctxloom's characteristic silent no-op wearing a sort's clothes: the caller
// asked for an ordering, got none, and was told nothing.
//
// The sibling ListProfiles (profiles.go) already had the correct shape — warn
// on an unknown sort_by and fall back to name — so this pin fixes the taxonomy
// in place rather than inventing a second one.
func TestSortContentInfos_UnknownSortByFallsBackToNameNotNoOp(t *testing.T) {
	// §11k hostility: the fixture must be in an order that is NOT already
	// name-ascending, otherwise a no-op sort would pass for the wrong reason.
	infos := []bundles.ContentInfo{
		{Name: "zebra", Source: "a"},
		{Name: "apple", Source: "b"},
		{Name: "mango", Source: "c"},
	}
	require.False(t, namesAreAscendingW76(infos),
		"fixture must start out of order, else a no-op sort passes vacuously")

	sortContentInfos(infos, "bogus-field", "asc")

	assert.Equal(t, []string{"apple", "mango", "zebra"}, namesOfW76(infos),
		"an unknown sort_by must still yield a deterministic ordering, not the input order")
}

// U084-F13 (half one, second face): the fallback must respect sort_order too —
// a "desc" request with an unknown field must not silently become ascending.
func TestSortContentInfos_UnknownSortByHonoursDescendingOrder(t *testing.T) {
	infos := []bundles.ContentInfo{
		{Name: "apple"},
		{Name: "zebra"},
		{Name: "mango"},
	}
	// §11k hostility: start ASCENDING-leaning so a no-op cannot look descending.
	require.NotEqual(t, []string{"zebra", "mango", "apple"}, namesOfW76(infos),
		"fixture must not already be in the asserted order")

	sortContentInfos(infos, "bogus-field", "desc")

	assert.Equal(t, []string{"zebra", "mango", "apple"}, namesOfW76(infos))
}

// U084-F13 (half two): containsTag folded the case of each TAG but not of the
// QUERY, so it carried an undocumented "caller must lowercase query"
// precondition. Every in-tree caller happens to honour it (ListFragments and
// SearchContent both pre-lower the query), which is exactly why it is a trap:
// the next caller inherits a silent false-negative with nothing to warn them.
//
// §11o: this defect is NOT observable at any public seam today — both public
// callers pre-lower — so this pin is deliberately at the UNIT altitude. The
// public-seam altitude is covered by the sort pins above; there is no
// public-seam pin for this half because there is no public seam that can reach
// it, and inventing one would mean removing the callers' own ToLower.
func TestContainsTag_QueryCaseIsNotACallerPrecondition(t *testing.T) {
	const query = "GoLang"
	// §11k hostility: the query must genuinely carry upper case, and the tag
	// must genuinely be lower case, or the assertion proves nothing.
	require.NotEqual(t, strings.ToLower(query), query, "query fixture must be mixed-case")
	tags := []string{"golang", "best-practices"}
	for _, tag := range tags {
		require.Equal(t, strings.ToLower(tag), tag, "tag fixture must be lower-case")
	}

	assert.True(t, containsTag(tags, query),
		"containsTag must fold the query's case itself rather than require the caller to")
}

// U084-F13 (half one) at the public seam: ListFragments is the exported entry
// point that hands sort_by straight through, so an unknown value must still
// come back ordered.
func TestListFragments_UnknownSortByStillReturnsOrderedResults(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	res, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		SortBy: "bogus-field",
		Loader: loader,
	})
	require.NoError(t, err)
	require.Equal(t, 4, res.Count)

	names := make([]string, 0, len(res.Fragments))
	for _, f := range res.Fragments {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"golang", "python", "security", "testing"}, names,
		"an unknown sort_by must fall back to name order, not the loader's order")
}

func namesOfW76(infos []bundles.ContentInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}

func namesAreAscendingW76(infos []bundles.ContentInfo) bool {
	for i := 1; i < len(infos); i++ {
		if strings.ToLower(infos[i-1].Name) > strings.ToLower(infos[i].Name) {
			return false
		}
	}
	return true
}

// U084-F18 (half one): PurgeExtractedBundles collapsed EVERY os.Stat failure on
// the bundles cache root into `return 0, nil` — a permission error, a broken
// mount and "the directory simply isn't there" were all reported as "nothing to
// do". The caller (cli.purgeLegacyBundles) warns on a non-nil error and
// continues, so there was never a reason for the silence.
func TestPurgeExtractedBundles_UnreadableRootIsNotSilentlyNothingToDo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the 0000 mode bit, so the fixture cannot be made hostile")
	}
	dir := t.TempDir()
	bundlesRoot := paths.CacheBundlesPath(dir)
	require.NoError(t, os.MkdirAll(bundlesRoot, 0o755))

	// Seal the PARENT so stat of bundlesRoot itself fails with EACCES rather
	// than ENOENT.
	parent := filepath.Dir(bundlesRoot)
	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	// §11k hostility: prove the fixture is actually hostile from the
	// code-under-test's vantage point — stat must fail, and NOT with
	// fs.ErrNotExist, or this pin would be asserting the legitimate no-op.
	_, statErr := os.Stat(bundlesRoot)
	require.Error(t, statErr, "fixture is not hostile: stat succeeded")
	require.False(t, errors.Is(statErr, fs.ErrNotExist),
		"fixture is not hostile: stat failed with ErrNotExist, which is the legitimate no-op")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
	removed, err := PurgeExtractedBundles(cfg)

	assert.Zero(t, removed)
	require.Error(t, err, "an unreadable bundles root must be reported, not treated as 'nothing to do'")
	assert.Contains(t, err.Error(), bundlesRoot, "the error must name the path it could not read")
}

// U084-F18 (half one, control): the ordinary "cache directory was never
// created" case must stay a silent, error-free no-op. Without this the fix
// above could over-report.
func TestPurgeExtractedBundles_MissingRootStaysAQuietNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})

	removed, err := PurgeExtractedBundles(cfg)

	require.NoError(t, err)
	assert.Zero(t, removed)
}

// U084-F18 (half two): the empty-directory prune ran inside a PRE-order
// WalkDir callback, so a parent that held one now-empty child was not itself
// empty when it was visited and survived the pass. Only the deepest level was
// ever cleared; a nested chain needed one extra run per level.
func TestPurgeExtractedBundles_PrunesNestedEmptyDirsInOnePass(t *testing.T) {
	dir := t.TempDir()
	bundlesRoot := paths.CacheBundlesPath(dir)
	outer := filepath.Join(bundlesRoot, "forge-a")
	inner := filepath.Join(outer, "owner", "repo")
	require.NoError(t, os.MkdirAll(inner, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(inner, "pulled.yaml"), []byte(
		"description: pulled\n_source:\n  sha: abc123\n"), 0o644))

	// §11k hostility: the chain must be genuinely more than one level deep,
	// otherwise a single-level prune would pass for the wrong reason.
	rel, relErr := filepath.Rel(bundlesRoot, inner)
	require.NoError(t, relErr)
	require.Greater(t, len(strings.Split(rel, string(filepath.Separator))), 1,
		"fixture must nest more than one level below the bundles root")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
	removed, err := PurgeExtractedBundles(cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	for _, gone := range []string{inner, filepath.Dir(inner), outer} {
		_, statErr := os.Stat(gone)
		assert.True(t, os.IsNotExist(statErr),
			"%s should have been pruned in the same pass", gone)
	}
	_, rootErr := os.Stat(bundlesRoot)
	assert.NoError(t, rootErr, "the bundles root itself must survive")
}

// U084-F09: NewRepoCache built its per-forge resolver inside
// `if registry, err := remote.NewRegistry(...); err == nil { ... }` — so a
// remotes.yaml that exists but cannot be parsed or read dropped every
// per-forge token_env with no warning at all. remote.NewRegistry already
// swallows os.IsNotExist internally, so a non-nil error from it is
// unambiguously "the file is there and broken", never "there is no file";
// the swallow could only ever hide a real failure.
//
// The user-visible consequence is a private-repo clone failing much later
// with a git auth error that says nothing about remotes.yaml.
func TestNewRepoCache_MalformedRemotesRegistryWarnsRatherThanSilentlyDroppingTokenEnv(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	remotesPath := paths.RemotesPath(dir)
	require.NoError(t, os.MkdirAll(filepath.Dir(remotesPath), 0o755))
	require.NoError(t, os.WriteFile(remotesPath, []byte("remotes: [this is not: a mapping\n"), 0o644))

	// §11k hostility: prove the fixture is hostile from the code-under-test's
	// vantage point — NewRegistry must actually FAIL on it. A merely odd but
	// parseable file would make the assertion below vacuous.
	_, regErr := remote.NewRegistry(remotesPath)
	require.Error(t, regErr, "fixture is not hostile: remote.NewRegistry parsed it fine")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
	var cache *remote.RepoCache
	stderr := captureStderr(t, func() { cache = NewRepoCache(cfg) })

	assert.NotNil(t, cache, "the cache is still built — this is a warning, not a hard failure")
	assert.Contains(t, stderr, "remotes registry", "the drop must be announced")
	assert.Contains(t, stderr, remotesPath, "the warning must name the unreadable file")
	assert.Contains(t, stderr, "token_env", "the warning must say what was lost")
}

// U084-F09 (control): a project with NO remotes.yaml is the ordinary case and
// must stay silent, or the fix above trades one bug for a permanent nag.
func TestNewRepoCache_MissingRemotesRegistryStaysSilent(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, statErr := os.Stat(paths.RemotesPath(dir))
	require.True(t, os.IsNotExist(statErr), "fixture must have no remotes.yaml")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
	stderr := captureStderr(t, func() { _ = NewRepoCache(cfg) })

	assert.NotContains(t, stderr, "remotes registry",
		"a project that has simply never configured a remote must not be warned at")
}

// U084-F11: when EVERY backend the request asked for failed, ApplyHooks
// answered Status "partial", Backends [] and a nil error — so
// `ctxloom manage hooks install` printed "Hooks partial for: []" and exited 0
// having written nothing at all. That is ctxloom's characteristic silent
// no-op: exit 0, a success-shaped message, zero bytes on disk.
//
// Total failure is now Status "failed" plus a non-nil error, which every
// caller already routes (manage.go returns it, trust.go and mcp_server.go
// warn). Partial success — at least one backend took — is deliberately
// untouched and still a nil error; the control below pins that.
func TestApplyHooks_TotalFailureIsNotReportedAsPartialSuccess(t *testing.T) {
	readOnly := afero.NewReadOnlyFs(afero.NewMemMapFs())
	const workDir = "/project"

	loader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
			}},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           readOnly,
		ConfigLoader: loader,
		WorkDir:      workDir,
	})

	// §11k hostility: prove the fixture actually defeated the write from the
	// code-under-test's vantage point. If a settings file HAD appeared, or no
	// per-backend error had been recorded, the assertions below would be
	// asserting something other than total failure.
	require.NotNil(t, result)
	require.NotEmpty(t, result.Errors, "fixture is not hostile: no backend actually failed")
	require.Empty(t, result.Backends, "fixture is not hostile: a backend still succeeded")
	exists, existsErr := afero.Exists(readOnly, workDir+"/.claude/settings.json")
	require.NoError(t, existsErr)
	require.False(t, exists, "fixture is not hostile: settings.json was written anyway")

	require.Error(t, err, "an apply that configured NOTHING must not return a nil error")
	assert.Equal(t, "failed", result.Status,
		`"partial" needs something on both sides of it; nothing was applied`)
}

// U084-F11 (control): genuine partial success — one backend took, another did
// not — must stay a nil error and Status "partial". The F11 fix must not
// promote every per-backend hiccup into a hard failure.
func TestApplyHooks_PartialSuccessStaysANilError(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	loader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
			}},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: loader,
		WorkDir:      tmpDir,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Backends, "control fixture must have at least one backend succeed")
	assert.Equal(t, "applied", result.Status)
}
