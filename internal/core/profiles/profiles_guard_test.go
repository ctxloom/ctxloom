package profiles

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedTestProfile returns a loader (over memfs) seeded with one remote profile
// carrying the synthetic "<remote>:" sentinel path, plus the seeded profile.
func seedTestProfile(t *testing.T) (*Loader, *Profile, afero.Fs) {
	t.Helper()
	canonical := "https://github.com/alice/ctxloom@profiles/dev"
	p := &Profile{
		Name:        canonical,
		Path:        SeededProfilePathPrefix + canonical + "@abc123",
		Description: "seeded remote profile",
	}
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/bundles/project/profiles", 0o755))
	loader := bundleLoader(t, fs,
		WithSeededProfiles(map[string]*Profile{canonical: p}))
	return loader, p, fs
}

// TestSave_RejectsSeededRemoteProfile is the paired guard for wiring the
// remote-profile seed into operations' loader: Save must refuse the sentinel
// path instead of MkdirAll-ing a junk "./<remote>:https:/..." tree and
// reporting success for an edit that evaporates on the next pull.
func TestSave_RejectsSeededRemoteProfile(t *testing.T) {
	loader, p, fs := seedTestProfile(t)

	err := loader.Save(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")

	// Nothing was created on disk — especially no directory derived from the
	// sentinel path.
	entries, err := afero.ReadDir(fs, "/bundles/project/profiles")
	require.NoError(t, err)
	assert.Empty(t, entries)
	exists, _ := afero.DirExists(fs, "<remote>:https:")
	assert.False(t, exists, "no junk directory derived from the sentinel path")

	// The sentinel path must survive (Save sets profile.Path on success;
	// failure must not clobber it).
	assert.True(t, IsSeededPath(p.Path))
}

// TestDelete_RejectsSeededRemoteProfile mirrors the Save guard: a seeded
// remote profile has no local file, so Delete must fail with a clear
// read-only error rather than attempting fs.Remove on the sentinel.
func TestDelete_RejectsSeededRemoteProfile(t *testing.T) {
	loader, p, _ := seedTestProfile(t)

	err := loader.Delete(p.Name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
}

// TestLoad_RejectsTraversalNames verifies profile names are confined to the
// profiles directories on the READ path too: names arrive from MCP tools and
// CLI args, and "../..."-shaped names previously resolved files outside the
// tree (Delete then removed them).
func TestLoad_RejectsTraversalNames(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/bundles/project/profiles", 0o755))
	// A target OUTSIDE the profiles dir that a traversal name would reach.
	testsupport.WriteFileString(t, fs, "/secret.yaml", "description: outside\n", 0o644)
	loader := bundleLoader(t, fs)

	for _, name := range []string{"../secret", "../../secret", "/secret"} {
		_, err := loader.Load(name)
		require.Errorf(t, err, "Load(%q) must be rejected", name)
		assert.Contains(t, err.Error(), "invalid profile name")
	}

	// Legitimate names still pass validation (then fail as plain not-found).
	_, err := loader.Load("..hidden")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid profile name")
}

// TestDelete_RejectsTraversalNames verifies a traversal name cannot delete a
// file outside the profiles directories (Delete resolves through Load, which
// now validates).
func TestDelete_RejectsTraversalNames(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/bundles/project/profiles", 0o755))
	testsupport.WriteFileString(t, fs, "/secret.yaml", "description: outside\n", 0o644)
	loader := bundleLoader(t, fs)

	err := loader.Delete("../secret")
	require.Error(t, err)

	exists, _ := afero.Exists(fs, "/secret.yaml")
	assert.True(t, exists, "the outside file must survive a traversal delete attempt")
}

// TestLoad_RemoteRefsStillPassValidation pins that a canonical remote ref —
// which carries "://" and "@" — is not caught by the traversal guard: it is a
// valid remote address, so an unseeded lookup surfaces a clean not-found (run a
// pull), never an "invalid profile name" rejection.
func TestLoad_RemoteRefsStillPassValidation(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/bundles/project/profiles", 0o755))
	loader := bundleLoader(t, fs)

	_, err := loader.Load("https://github.com/alice/ctxloom@bundles/dev")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid profile name")
}

// TestLoad_ReturnsTheSharedInstance pins the ownership contract the Load doc
// states: a loaded profile is the ONE shared instance every reader receives,
// so the write paths work on the profile's file, never on this instance.
func TestLoad_ReturnsTheSharedInstance(t *testing.T) {
	loader, seeded, _ := seedTestProfile(t)

	first, err := loader.Load(seeded.Name)
	require.NoError(t, err)
	second, err := loader.Load(seeded.Name)
	require.NoError(t, err)
	assert.Same(t, first, second, "a loaded profile is the one shared instance")
}
