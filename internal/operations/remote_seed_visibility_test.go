package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedRemoteFixture builds a real git repo carrying one bundle that SHIPS a
// bundle profile (`profiles:` item), locks the bundle in a fresh appDir's
// lockfile, and returns the cfg, the bundle profile's canonical identity, and
// the bundle's lockfile fetch address. This is the full reference-only remote path:
// content lives only in the clone cache at the locked SHA, visible exclusively
// through the lockfile-built bundle seed. (Top-level @profiles/ distribution was
// retired — profiles arrive only inside bundles.)
func seedRemoteFixture(t *testing.T) (cfg *config.Config, profileRef, bundleRef string) {
	t.Helper()
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".ctxloom", "content", "bundles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".ctxloom", "content", "bundles", "tools.yaml"),
		[]byte("version: 1.0.0\ndescription: remote tools bundle\nprofiles:\n  dev:\n    description: remote dev profile\n    tags: [go]\n"), 0o644))
	_, err = wt.Add(".ctxloom/content/bundles/tools.yaml")
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
	profileRef = "ctxloom+file://" + repoDir + "//bundles/tools#profiles/dev"
	lock.AddEntry(remote.ItemTypeBundle, bundleRef, entry)
	require.NoError(t, lm.Save(lock))

	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}}), profileRef, bundleRef
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
