package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedRemoteFixture builds a real git repo carrying one bundle that SHIPS a
// bundle profile (`profiles:` item), locks the bundle in a fresh appDir's
// lockfile, and returns the cfg, the bundle profile's canonical identity, and
// the bundle's lockfile fetch address.
//
// The bytes a session actually reads come from the INSTALLED CACHE tree, not
// the clone: format v2 publishes only trees, so remote.BundleReader's direct
// clone-read path is unconditionally refused (no per-pin document/tree flag is
// left to dispatch on — see the removal note on TestLoadRemoteBundleSeed_
// FullLoad in internal/core/config), and config.treeBundleReaders reads whatever
// `deps pull` already installed at Reference.LocalTreePath. The git repo is
// still built and committed to so the fixture's lockfile SHA/URL are real —
// callers that inspect provenance (SourceRef, a git-backed pin) see honest
// values — but this test double INSTALLS the same bytes directly, standing in
// for the pull a real session would have run first.
func seedRemoteFixture(t *testing.T) (cfg *config.Config, profileRef, bundleRef string) {
	t.Helper()
	testsupport.Isolate(t)

	const bundleBody = "version: 1.0.0\ndescription: remote tools bundle\nprofiles:\n  dev:\n    description: remote dev profile\n    tags: [go]\n"

	repoDir := filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(authoredV2(filepath.Join(repoDir, paths.AppDirName)), 0o755))
	bundletree.WriteOS(t, authoredV2(filepath.Join(repoDir, paths.AppDirName)), "tools", bundleBody)
	_, err = wt.Add(repoV2("tools"))
	require.NoError(t, err)
	commit, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	sha := commit.String()
	repoURL := "file://" + repoDir

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	lm := remote.NewLockfileManager(appDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	entry := remote.LockEntry{SHA: sha, URL: repoURL, FetchedAt: time.Now().UTC()}
	bundleRef = repoURL + "@bundles/tools"
	// The lockfile keys on the FETCH address (bundleRef); the profile is
	// addressed by the bundle's canonical IDENTITY, which is what the seed and
	// every listing carry.
	profileRef = canonicalRef(t, "ctxloom+file://"+repoDir+"//bundles/tools") + "#profiles/dev"
	lock.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, bundleRef), entry)
	require.NoError(t, lm.Save(lock))

	// Stand in for `deps pull`: install a TRUE TREE at the path
	// config.treeBundleReader actually reads from. A tree's own envelope may
	// declare NOTHING inline (readEnvelope refuses one that still does,
	// unconditionally — there is no document fallback on this path, unlike
	// the local reader's treeFormEnvelope), so the profile item travels as a
	// real file, written through the production content.Writer rather than
	// hand-rolled, and bundle.yaml is written separately with only what a
	// tree's envelope may legally carry.
	ref, err := remote.ParseReference(bundleRef)
	require.NoError(t, err)
	installDir, terr := ref.LocalTreePath(appDir)
	require.NoError(t, terr)
	require.NoError(t, os.MkdirAll(installDir, 0o755))

	src, err := bundles.ParseBundle([]byte(bundleBody))
	require.NoError(t, err)
	bundletree.WriteBundle(t, afero.NewOsFs(), filepath.Dir(installDir), "tools", src)
	require.NoError(t, os.WriteFile(filepath.Join(installDir, "bundle.yaml"),
		[]byte("version: 1.0.0\ndescription: remote tools bundle\n"), 0o644))

	return published(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}})), profileRef, bundleRef
}

// TestListProfiles_IncludesLockfileSeededRemoteProfile pins finding the seed
// through operations' own loader factory: a locked remote profile must appear
// in `profile list` exactly as it does for context assembly
// (config.GetProfileLoader) — the two factories share the seed.
func TestListProfiles_IncludesLockfileSeededRemoteProfile(t *testing.T) {
	cfg, profileRef, _ := seedRemoteFixture(t)

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{})
	require.NoError(t, err)

	names := make([]string, 0, len(result.Profiles))
	for _, p := range result.Profiles {
		names = append(names, p.Name)
	}
	assert.Contains(t, names, profileRef,
		"a lockfile-seeded remote profile must be visible to profile list")
}

// TestGetProfile_LoadsLockfileSeededRemoteProfile pins `profile show
// <canonical-ref>`: the seeded entry must load instead of failing with the
// misleading "remote profile has no lockfile entry" error.
func TestGetProfile_LoadsLockfileSeededRemoteProfile(t *testing.T) {
	cfg, profileRef, _ := seedRemoteFixture(t)

	result, err := GetProfile(context.Background(), cfg, GetProfileRequest{Name: profileRef})
	require.NoError(t, err, "a locked remote profile must load via its canonical ref")
	assert.Equal(t, "remote dev profile", result.Description)
}

// TestUpdateProfile_RejectsSeededRemoteProfile is the paired guard for the
// seed wiring: `profile modify <remote-ref>` must fail up front as read-only —
// NOT mutate the shared in-memory seed and then "succeed" while the edit
// evaporates (Save would have MkdirAll'd a junk "<remote>:..." tree).
func TestUpdateProfile_RejectsSeededRemoteProfile(t *testing.T) {
	cfg, profileRef, _ := seedRemoteFixture(t)

	desc := "tampered"
	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:        profileRef,
		Description: &desc,
		AddTags:     []string{"new"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")

	// The shared seed must be untouched.
	got, gerr := GetProfile(context.Background(), cfg, GetProfileRequest{Name: profileRef})
	require.NoError(t, gerr)
	assert.Equal(t, "remote dev profile", got.Description, "the in-memory seed must not be mutated")
	assert.NotContains(t, got.Tags, "new")
}

// TestDeleteProfile_RejectsSeededRemoteProfile: remote profiles have no local
// file; delete must fail clearly instead of pretending.
func TestDeleteProfile_RejectsSeededRemoteProfile(t *testing.T) {
	cfg, profileRef, _ := seedRemoteFixture(t)

	_, err := DeleteProfile(context.Background(), cfg, DeleteProfileRequest{Name: profileRef})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
}

// TestGetBundle_LoadsLockfileSeededRemoteBundle pins `bundle show
// <canonical-ref>`: GetBundle is a read path and must use the seeded loader
// like ListBundles/GetItemContent — the unseeded store reported "not found"
// for every remote bundle the list had just displayed.
func TestGetBundle_LoadsLockfileSeededRemoteBundle(t *testing.T) {
	cfg, _, bundleRef := seedRemoteFixture(t)

	bundle, err := GetBundle(cfg, bundleRef)
	require.NoError(t, err, "a locked remote bundle must load via its canonical ref")
	assert.Equal(t, "remote tools bundle", bundle.Description)
}

// TestSearchContent_FindsDirectoryAndSeededProfiles pins search_content's
// profile searcher to the loader ListProfiles uses: directory profiles (the
// common case) and lockfile-seeded remote profiles must both be searchable,
// not just inline config definitions.
func TestSearchContent_FindsDirectoryAndSeededProfiles(t *testing.T) {
	cfg, profileRef, _ := seedRemoteFixture(t)

	// Add a directory profile alongside the seeded remote one.
	profilesDir := filepath.Join(cfg.GetAppPaths()[0], "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "local-dev.yaml"),
		[]byte("description: local directory profile\n"), 0o644))

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query: "dev",
		Types: []string{"profile"},
	})
	require.NoError(t, err)

	names := make([]string, 0, len(result.Results))
	for _, r := range result.Results {
		names = append(names, r.Name)
	}
	assert.Contains(t, names, "local-dev", "directory profiles must be searchable")
	assert.Contains(t, names, profileRef, "seeded remote profiles must be searchable")
}
