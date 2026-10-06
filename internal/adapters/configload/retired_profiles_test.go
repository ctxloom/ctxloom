package configload

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// A project still holding the retired standalone profiles directory does not
// load: those profiles are no longer read, and launching past them would run
// every agent naming one on context that silently vanished. The refusal is
// the fix — the exact manual move into the project bundle — not a move made
// on the user's behalf.
func TestRead_RetiredProfilesDirIsAHardError(t *testing.T) {
	const appDir = "/proj/.ctxloom"
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, filepath.Join(paths.ProfilesPath(appDir), "dev.yaml"), "bundles: [x]\n", 0o644)

	_, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.ErrorIs(t, err, errRetiredProfilesDir)
	assert.Contains(t, err.Error(), "git mv "+paths.ProfilesPath(appDir)+"/*.yaml "+bundletree.ProjectProfilesPath(appDir)+"/",
		"the fix line names the exact move")
	assert.Contains(t, err.Error(), filepath.Join(filepath.Dir(bundletree.ProjectProfilesPath(appDir)), bundles.DirectoryFormManifest),
		"and the envelope the project bundle needs when it has none")

	exists, err := afero.Exists(fs, filepath.Join(paths.ProfilesPath(appDir), "dev.yaml"))
	require.NoError(t, err)
	assert.True(t, exists, "nothing is moved for the user")
}

// The control: a project whose profiles already live in the project bundle
// loads.
func TestRead_ProjectBundleProfilesLoad(t *testing.T) {
	const appDir = "/proj/.ctxloom"
	fs := afero.NewMemMapFs()
	bundletree.WriteDirProfiles(t, fs, appDir, map[string]any{"dev": map[string]any{"bundles": []string{"x"}}})

	_, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.NoError(t, err)
}
