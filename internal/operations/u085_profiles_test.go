package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
)

// u085ProfileProject builds an app dir with one real local profile, on the OS
// filesystem so the loader that reads it needs no injection.
func u085ProfileProject(t *testing.T) *config.Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "profiles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "profiles", "local-one.yaml"),
		[]byte("name: local-one\ndescription: real\n"), 0o644))
	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// TestUpdateProfile_PreservesLoaderError pins U085-F14 for the write path:
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

// TestLoadLocalProfile_PreservesLoaderError pins U085-F14 for the edit/export
// path. The remote-reference branch is deliberate and stays; the LOCAL branch
// flattened the loader error the same way UpdateProfile did.
func TestLoadLocalProfile_PreservesLoaderError(t *testing.T) {
	cfg := u085ProfileProject(t)
	fs := afero.NewOsFs()

	_, err := loadLocalProfile(cfg, fs, "no-such-local")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrProfileNotFound,
		"the sentinel must survive the local-only edit/export path too")

	// The deliberate remote-reference branch is unaffected: it answers a
	// DIFFERENT question ("it exists, just not locally") and keeps its hint.
	_, rerr := loadLocalProfile(cfg, fs, "https://github.com/o/r@bundles/b#profiles/p")
	require.Error(t, rerr)
	assert.Contains(t, rerr.Error(), "local-only")
}
