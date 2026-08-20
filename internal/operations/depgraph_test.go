package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// newTestWalker builds a depWalker whose remote reads are served by fetcher.
// resolveHash is the identity resolver — a ref's already-concrete version
// expression IS its hash for these unit tests, which construct refs with a
// pre-pinned "@<hash>" suffix directly (production no longer carries a
// nil-means-identity mode; this is the resolver that used to be implicit).
func newTestWalker(fetcher remote.Fetcher) *depWalker {
	return &depWalker{
		ctx:     context.Background(),
		factory: func(string, remote.AuthConfig) (remote.Fetcher, error) { return fetcher, nil },
		resolveHash: func(ref *remote.Reference) (string, string, remote.SelectorKind, bool) {
			return ref.ContentVersion, "", "", true
		},
		pins:       map[string]PinnedRef{},
		hashes:     map[string]map[string]struct{}{},
		visited:    map[string]struct{}{},
		unexpanded: map[string]struct{}{},
	}
}

func TestDepWalker_RecordsAndConflicts(t *testing.T) {
	t.Run("local refs are not tracked as remote pins", func(t *testing.T) {
		w := newTestWalker(remote.NewMockFetcher())
		w.record("ctxloom:local@bundles/x", remote.ItemTypeBundle)
		pins, conflicts, _ := w.result()
		assert.Empty(t, pins)
		assert.Empty(t, conflicts)
	})

	t.Run("same identity at two hashes is a conflict", func(t *testing.T) {
		w := newTestWalker(remote.NewMockFetcher())
		w.record("https://github.com/o/r@bundles/demo@aaaaaaa", remote.ItemTypeBundle)
		w.record("https://github.com/o/r@bundles/demo@bbbbbbb", remote.ItemTypeBundle)
		pins, conflicts, _ := w.result()

		require.Len(t, conflicts, 1)
		assert.Equal(t, "https://github.com/o/r@bundles/demo", conflicts[0].Item)
		assert.Equal(t, []string{"aaaaaaa", "bbbbbbb"}, conflicts[0].Hashes)
		require.Len(t, pins, 1, "the conflicted item still appears once in the pin set")
	})

	t.Run("same identity at the same hash is not a conflict", func(t *testing.T) {
		w := newTestWalker(remote.NewMockFetcher())
		w.record("https://github.com/o/r@bundles/demo@aaaaaaa", remote.ItemTypeBundle)
		w.record("https://github.com/o/r@bundles/demo@aaaaaaa", remote.ItemTypeBundle)
		_, conflicts, _ := w.result()
		assert.Empty(t, conflicts)
	})
}

func TestDepWalker_WalksRemoteParentClosure(t *testing.T) {
	// Local profile P pins bundle X@h1 directly AND has a remote parent that is a
	// bundle profile A#profiles/a; the bundle A ships profile `a`, which composes
	// the SAME bundle X at a DIFFERENT hash h2 — a diamond conflict that must
	// surface through the bundle-profile-parent walk (the parent bundle is
	// fetched, its named profile extracted, and its closure walked).
	const (
		urlX = "https://github.com/x/repo"
		urlA = "https://github.com/a/repo"
	)
	bundleA := "version: \"1.0.0\"\nprofiles:\n  a:\n    bundles:\n      - " + urlX + "@bundles/x@h2222222\n"
	fetcher := remote.NewMockFetcher().
		WithFile(".ctxloom/content/bundles/akit.yaml", []byte(bundleA))

	w := newTestWalker(fetcher)
	root := &profiles.Profile{
		Name:    "local",
		Bundles: []string{urlX + "@bundles/x@h1111111"},
		Parents: []string{urlA + "@bundles/akit@hAAAAAAA#profiles/a"},
	}
	w.walkProfile(root, remote.LocalSource, "")

	pins, conflicts, _ := w.result()

	// X (from both P and the bundle profile) and the parent bundle akit are pinned.
	identities := map[string]string{}
	for _, p := range pins {
		identities[p.Identity] = p.Hash
	}
	assert.Contains(t, identities, urlX+"@bundles/x")
	assert.Contains(t, identities, urlA+"@bundles/akit")

	require.Len(t, conflicts, 1)
	assert.Equal(t, urlX+"@bundles/x", conflicts[0].Item)
	assert.Equal(t, []string{"h1111111", "h2222222"}, conflicts[0].Hashes)
}

// TestDepWalker_RecurseParent_MalformedRefRecordsUnexpanded pins the
// worst-of-the-three gap: a parent ref that fails to even PARSE used to lose
// its whole subtree with NO diagnostic at all (not even a warning) — worse
// than the remote-fetch failures a few lines below it in the same function,
// which already warn-and-record via markUnexpanded.
func TestDepWalker_RecurseParent_MalformedRefRecordsUnexpanded(t *testing.T) {
	w := newTestWalker(remote.NewMockFetcher())
	w.recurseParent("::::not-a-valid-reference-at-all")
	_, _, unexpanded := w.result()
	require.Len(t, unexpanded, 1)
	assert.Equal(t, "::::not-a-valid-reference-at-all", unexpanded[0])
}

// TestDepWalker_RecurseParent_LocalLoadFailureRecordsUnexpanded pins the
// second gap: an unreadable LOCAL parent profile is the same
// "subtree missing from pins" hazard as an unreadable REMOTE one (which this
// function already handles correctly a few lines below), but was silently
// skipped instead of recorded.
func TestDepWalker_RecurseParent_LocalLoadFailureRecordsUnexpanded(t *testing.T) {
	w := newTestWalker(remote.NewMockFetcher())
	w.loader = profiles.NewLoader([]string{t.TempDir()}) // empty dir: Load always fails
	w.recurseParent("ctxloom:local@bundles/does-not-exist")
	_, _, unexpanded := w.result()
	require.Len(t, unexpanded, 1)
	assert.Equal(t, "does-not-exist", unexpanded[0])
}

// TestDepWalker_RecurseParent_NotBundleProfileRefRecordsUnexpanded pins the
// third gap: a remote parent ref that isn't a bundle-profile reference (a
// retired top-level @profiles/ parent, or a malformed one) loses its subtree
// exactly like the two cases above, and the function's own comment already
// says so ("its subtree is gone") without ever recording it.
func TestDepWalker_RecurseParent_NotBundleProfileRefRecordsUnexpanded(t *testing.T) {
	w := newTestWalker(remote.NewMockFetcher())
	// A well-formed, PARSEABLE remote bundle ref that is NOT the
	// <url>@bundles/x#profiles/y shape SplitBundleProfileRef requires (no
	// "#profiles/" selector at all) — it must parse successfully and reach
	// SplitBundleProfileRef to actually exercise that function's `!ok`
	// branch, rather than failing earlier at ParseReference.
	ref := "https://github.com/o/r@bundles/x@hAAAAAAA"
	w.recurseParent(ref)
	_, _, unexpanded := w.result()
	require.Len(t, unexpanded, 1)
	assert.Equal(t, ref, unexpanded[0])
}

// TestNamedRoots_LoadFailureRecordsUnexpanded pins the root-level half of the
// gap: a profile root that fails to load NARROWS the closure (the entry
// is silently dropped from `roots`), but before this fix nothing recorded
// which name was lost — only the warning printed to stderr. Callers rebuild
// the lockfile wholesale from `roots`, and the `unexpanded` return is the ONE
// mechanism protecting existing lockfile entries from being erased when the
// closure narrows; a root failure had no way to reach it.
func TestNamedRoots_LoadFailureRecordsUnexpanded(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})
	loader := profiles.NewLoader([]string{t.TempDir()}) // empty dir: nothing loads

	roots, unexpanded := namedRoots(cfg, loader, []string{"missing-profile"})
	assert.Empty(t, roots)
	require.Len(t, unexpanded, 1)
	assert.Equal(t, "missing-profile", unexpanded[0])
}

// TestNamedRoots_LoadableProfileIsNeverUnexpanded is the negative companion to
// the missing-profile case above: a profile the loader CAN load becomes a root
// and must never appear in the unexpanded set.
//
// It used to pin the same outcome for an INLINE config.yaml definition, which
// short-circuited before the loader was consulted at all. That arm is retired —
// the loader is the only lookup — so the claim is now about a profile that
// resolves, and the loader is pointed at the directory the profile lives in
// rather than at an empty one.
func TestNamedRoots_LoadableProfileIsNeverUnexpanded(t *testing.T) {
	appDir := t.TempDir() + "/" + paths.AppDirName
	cfg := cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"onfile": {Bundles: []string{"ctxloom:local@bundles/x"}},
	}, config.Fixture{})
	loader := profiles.NewLoader([]string{paths.ProfilesPath(appDir)})

	roots, unexpanded := namedRoots(cfg, loader, []string{"onfile"})
	assert.Len(t, roots, 1)
	assert.Empty(t, unexpanded, "a profile that loads is expanded, never reported as missing")
}

// TestFlattenDependencies_RootLoadFailureSurfacesInUnexpanded is the
// integration-level pin: FlattenDependencies (the public entry point
// UpgradeDependencies and `remote lock` both call) must merge namedRoots'
// own failures into its returned unexpanded set, not just the walker's.
func TestFlattenDependencies_RootLoadFailureSurfacesInUnexpanded(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})
	testsupport.Isolate(t)

	_, _, unexpanded := FlattenDependencies(context.Background(), cfg, []string{"missing-profile"})
	require.Len(t, unexpanded, 1)
	assert.Equal(t, "missing-profile", unexpanded[0])
}

func TestConflictError(t *testing.T) {
	assert.NoError(t, ConflictError(nil))
	err := ConflictError([]DependencyConflict{{Item: "https://github.com/o/r@bundles/demo", Hashes: []string{"aaaaaaa1", "bbbbbbb2"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundles/demo")
	assert.Contains(t, err.Error(), "aaaaaaa") // short form
}

// TestFlattenDependencies_UnreadableLockfileIsReported pins a claim that is
// PARTIAL: the mechanism is real, the consequence is not.
//
// Real: flattenProfileRoots loads the active lockfile with `active, _ :=`,
// discarding the error, and that lockfile is the closure walk's RESOLUTION
// ANCHOR — a held or unchanged-constraint entry is carried forward, and a
// resolution failure falls back to its last known SHA rather than dropping the
// item. Not real: "silently". FlattenDependencies builds its profile loader
// FIRST, and that seeds remote bundles from the same lockfile
// (config.loadRemoteBundleSeed), which reports an unreadable one fatal-class
// (strictness ClassBundle) naming the file and the fix — every time, before
// this load is ever reached. In strict mode the session refuses outright; in
// degraded mode the anchorless rebuild is still barred from being PERSISTED by
// Save's unreadable-file refusal.
//
// The comparison to the sibling upgrade path is also not actionable here:
// FlattenDependencies returns no error at all, and its signature is a public
// contract this row is not entitled to change. So this pins the property that
// makes the discard a decision rather than an oversight: the failure IS
// reported, exactly once, before resolution runs anchorless.
func TestFlattenDependencies_UnreadableLockfileIsReported(t *testing.T) {
	resetStrictness(t)
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "default",
		"bundles:\n  - https://github.com/test/repo@bundles/demo@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)

	lockPath := remote.NewLockfileManager(tmp).Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	require.NoError(t, os.WriteFile(lockPath, []byte("   \n"), 0o644))

	warnings := captureWarnings(t)
	FlattenDependencies(context.Background(), cfg, nil)

	out := warnings.String()
	assert.Contains(t, out, lockPath, "the failure names the file that could not be read")
	assert.Contains(t, out, "lockfile", "…as a lockfile failure, not some downstream symptom")
	assert.Equal(t, 1, strings.Count(out, "failed to load the remote lockfile"),
		"reported once — a second report from the resolution anchor's own load would say the same thing twice")
}

// TestFlattenDependencies_MissingLockfileIsSilent: no lock.yaml at all is the
// ordinary first-lock state, not a failure — Load hands back a fresh empty
// lockfile and nothing is lost, so nothing about the lockfile is said.
func TestFlattenDependencies_MissingLockfileIsSilent(t *testing.T) {
	resetStrictness(t)
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "default",
		"bundles:\n  - https://github.com/test/repo@bundles/demo@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)

	warnings := captureWarnings(t)
	FlattenDependencies(context.Background(), cfg, nil)
	assert.NotContains(t, warnings.String(), "lockfile")
}
