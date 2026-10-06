package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// u085ProfileProject builds an app dir with one real local profile, on the OS
// filesystem so the loader that reads it needs no injection.
func u085ProfileProject(t *testing.T) *config.Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, appDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "local-one.yaml"),
		[]byte("description: real\n"), 0o644))
	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// TestUpdateProfile_PreservesLoaderError pins the write path:
// UpdateProfile flattened the loader's error to a bare "profile %q not found",
// discarding the errs.ErrProfileNotFound sentinel every caller matches on and
// the actionable detail the loader attaches (the remote-pull hint for a
// bundle profile with no lockfile entry, the reserved-'#' explanation).
// GetProfile already returns it verbatim; the two must not disagree about the
// SAME failure.
func TestUpdateProfile_PreservesLoaderError(t *testing.T) {
	cfg := u085ProfileProject(t)
	desc := "x"

	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:        "no-such-profile",
		Description: &desc,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrProfileNotFound,
		"the sentinel must survive: callers branch on errors.Is, not on message text")
	assert.Contains(t, err.Error(), "no-such-profile", "the failing name must still be named")

	// GetProfile is the reference behaviour for the same input.
	_, gerr := GetProfile(context.Background(), cfg, GetProfileRequest{Name: "no-such-profile"})
	require.Error(t, gerr)
	assert.True(t, errors.Is(gerr, errs.ErrProfileNotFound))
}

// TestLoadLocalProfile_PreservesLoaderError pins the edit/export
// path. The remote-reference branch is deliberate and stays; the LOCAL branch
// flattened the loader error the same way UpdateProfile did.
func TestLoadLocalProfile_PreservesLoaderError(t *testing.T) {
	cfg := u085ProfileProject(t)

	_, err := loadLocalProfile(cfg, "no-such-local")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrProfileNotFound,
		"the sentinel must survive the local-only edit/export path too")

	// A remote bundle profile that is not installed keeps the sentinel and
	// its pull hint.
	_, rerr := loadLocalProfile(cfg, "https://github.com/o/r@bundles/b#profiles/p")
	require.ErrorIs(t, rerr, errs.ErrProfileNotFound)
	assert.Contains(t, rerr.Error(), "ctxloom deps pull")
}

// TestCreateProfile_FreshProjectCreatesTheProjectBundle pins the fresh-install
// path: a project with no project bundle yet gets one on its first profile
// write, and the profile lands in it as an item.
func TestCreateProfile_FreshProjectCreatesTheProjectBundle(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	_, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{Name: "fresh", Bundles: []string{"go-development"}})
	require.NoError(t, err)
	bundle := filepath.Join(paths.LocalBundlesPathFor(appDir, paths.LayoutV2), paths.ProjectBundleName)
	assert.FileExists(t, filepath.Join(bundle, bundles.DirectoryFormManifest))
	assert.FileExists(t, filepath.Join(bundle, paths.ProfilesDir, "fresh.yaml"))

	p, err := cfg.GetProfileLoader().Load("fresh")
	require.NoError(t, err, "the created profile resolves under its selector-less name")
	assert.Equal(t, []string{"go-development"}, p.Bundles)
}
