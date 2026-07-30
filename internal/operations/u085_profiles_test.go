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
	"github.com/ctxloom/ctxloom/internal/profiles"
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

// TestProfileLoaderFactories_AgreeUnderInjectedFS is U085-F11's parity test
// across BOTH loader factories, written before the collapse. They are near-
// identical, and where they DIVERGE the divergence is the defect: config's
// GetProfileLoader threads WithFS(c.fs) while operations' profileLoader never
// did, so under an injected filesystem the two disagreed about which profiles
// exist — operations' read the real OS filesystem while every directory it had
// just discovered came from the injected one.
func TestProfileLoaderFactories_AgreeUnderInjectedFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/injected/.ctxloom"
	require.NoError(t, fs.MkdirAll(filepath.Join(appDir, "profiles"), 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(appDir, "profiles", "injected.yaml"),
		[]byte("name: injected\ndescription: from the injected fs\n"), 0o644))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	cfg.SetFS(fs)

	fromConfig, cerr := cfg.GetProfileLoader().Load("injected")
	require.NoError(t, cerr, "config's factory resolves the injected profile")

	fromOps, oerr := profileLoader(cfg).Load("injected")
	require.NoError(t, oerr, "operations' factory must resolve the SAME profile as config's")
	assert.Equal(t, fromConfig.Description, fromOps.Description)

	names := func(l interface {
		List() ([]*profiles.Profile, error)
	}) []string {
		infos, err := l.List()
		require.NoError(t, err)
		out := make([]string, 0, len(infos))
		for _, p := range infos {
			out = append(out, p.Name)
		}
		return out
	}
	assert.Equal(t, names(cfg.GetProfileLoader()), names(profileLoader(cfg)),
		"the two factories must list the same profile set")
}

// TestProfileLoader_KeepsFreshInstallFallbackDir pins the ONE behaviour that is
// genuinely operations-only and must survive the collapse: on a fresh install
// no profiles directory exists yet, so GetProfileDirs returns nothing and the
// operations factory synthesizes <appPath>/profiles so a Save has somewhere to
// land.
func TestProfileLoader_KeepsFreshInstallFallbackDir(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	require.NoError(t, profileLoader(cfg).Save(&profiles.Profile{Name: "fresh", Description: "d"}))
	assert.FileExists(t, filepath.Join(appDir, "profiles", "fresh.yaml"))
}
