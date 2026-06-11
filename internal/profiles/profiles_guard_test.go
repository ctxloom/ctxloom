package profiles

import (
	"testing"

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
	require.NoError(t, fs.MkdirAll("/profiles", 0o755))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs),
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
	entries, err := afero.ReadDir(fs, "/profiles")
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
	require.NoError(t, fs.MkdirAll("/profiles", 0o755))
	// A target OUTSIDE the profiles dir that a traversal name would reach.
	require.NoError(t, afero.WriteFile(fs, "/secret.yaml", []byte("description: outside\n"), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

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
	require.NoError(t, fs.MkdirAll("/profiles", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/secret.yaml", []byte("description: outside\n"), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	err := loader.Delete("../secret")
	require.Error(t, err)

	exists, _ := afero.Exists(fs, "/secret.yaml")
	assert.True(t, exists, "the outside file must survive a traversal delete attempt")
}

// TestLoad_RemoteRefsStillPassValidation pins that canonical remote refs —
// which carry "://" and "@" — are not caught by the traversal guard: they
// normalize to their local materialized form before validation.
func TestLoad_RemoteRefsStillPassValidation(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs,
		"/profiles/github.com/alice/ctxloom/dev.yaml",
		[]byte("description: materialized remote\n"), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	p, err := loader.Load("https://github.com/alice/ctxloom@profiles/dev")
	require.NoError(t, err)
	assert.Equal(t, "materialized remote", p.Description)
}

// TestLoadFile_DedupesPendingUpgradesByPath verifies a legacy file loaded
// several times in one run (e.g. a shared legacy parent profile) yields ONE
// pending upgrade, not one consent prompt per load.
func TestLoadFile_DedupesPendingUpgradesByPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs,
		"/profiles/personal/legacy.yaml",
		[]byte("bundles:\n  - core-practices\n"),
		0o644))

	resolver := func(name string) string { return "personal" }
	urlResolver := func(alias string) string {
		if alias == "personal" {
			return "https://github.com/me/ctxloom"
		}
		return ""
	}
	loader := NewLoader([]string{"/profiles"}, WithFS(fs),
		WithRemoteResolver(resolver), WithRemoteURLResolver(urlResolver))

	for range 3 {
		_, err := loader.Load("personal/legacy")
		require.NoError(t, err)
	}

	pending := loader.PendingUpgrades()
	require.Len(t, pending, 1, "repeated loads of one legacy file must record one pending upgrade")
	assert.Equal(t, "/profiles/personal/legacy.yaml", pending[0].Path)
}
