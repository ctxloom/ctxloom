package profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// testBundlesRoot is the local bundles root the tests' loaders write under, and
// testProfilesDir the project bundle's profiles directory beneath it.
const (
	testBundlesRoot = "/bundles"
	testProfilesDir = testBundlesRoot + "/project/profiles"
)

// bundleLoader is the test-side mirror of config.loadBundleProfileSeed for one
// project bundle: every testProfilesDir/<name>.yaml on fs is decoded through
// Decode and seeded as the project bundle's profile of that name, at its real
// path, exactly as a local bundle's profiles reach the production loader.
func bundleLoader(t *testing.T, fs afero.Fs, opts ...LoaderOption) *Loader {
	t.Helper()
	return loaderAt(t, fs, testProfilesDir, opts...)
}

// loaderAt is bundleLoader over the project bundle whose profiles directory is
// profilesDir; the local bundles root is the directory holding that bundle.
func loaderAt(t *testing.T, fs afero.Fs, profilesDir string, opts ...LoaderOption) *Loader {
	t.Helper()
	// The project bundle exists: Save writes only into a local bundle that does.
	require.NoError(t, fs.MkdirAll(profilesDir, 0o755))
	seed := map[string]*Profile{}
	entries, err := afero.ReadDir(fs, profilesDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			path := filepath.Join(profilesDir, e.Name())
			data, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			p, err := Decode(data)
			require.NoError(t, err, path)
			p.Name = projectProfileRef(strings.TrimSuffix(e.Name(), ".yaml"))
			p.Path = path
			seed[p.Name] = p
		}
	}
	base := []LoaderOption{WithFS(fs), WithSeededProfiles(seed)}
	return NewLoader([]string{filepath.Dir(filepath.Dir(profilesDir))}, append(base, opts...)...)
}

// projectProfilesDir is a fresh project bundle's profiles directory on the OS
// filesystem, for tests that write profile documents with os.WriteFile.
func projectProfilesDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project", "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// osLoader is loaderAt on the OS filesystem.
func osLoader(t *testing.T, profilesDir string, opts ...LoaderOption) *Loader {
	t.Helper()
	return loaderAt(t, afero.NewOsFs(), profilesDir, opts...)
}

// writeProjectProfile writes a project-bundle profile document for
// bundleLoader to seed.
func writeProjectProfile(t *testing.T, fs afero.Fs, name, doc string) {
	t.Helper()
	require.NoError(t, fs.MkdirAll(testProfilesDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(testProfilesDir, name+".yaml"), []byte(doc), 0o644))
}
