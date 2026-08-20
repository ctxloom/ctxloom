package operations

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// TestIsRemoteReference_RecognizesEveryFetchableSpelling pins the predicate that
// decides whether a ref is looked up as a LOCAL profile name or left alone as a
// remote address.
//
// Getting it wrong is not a near miss: a canonical ref that answers "not remote"
// falls through to loader.Exists and is reported as `parent profile %q not
// found`, naming the one place the profile was never going to be.
func TestIsRemoteReference_RecognizesEveryFetchableSpelling(t *testing.T) {
	remoteRefs := map[string]string{
		"canonical git":           "ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/ai-developer",
		"canonical git + version": "ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/ai-developer@main",
		"canonical file":          "ctxloom+file:///srv/repo//bundles/x",
		"retired https":           "https://github.com/ctxloom/ctxloom-default@bundles/ai-developer",
		"retired http":            "http://example.com/o/r@bundles/b",
		"retired file":            "file:///srv/repo@bundles/x",
		"scp-like git":            "git@github.com:o/r.git",
	}
	for name, ref := range remoteRefs {
		t.Run(name, func(t *testing.T) {
			assert.True(t, isRemoteReference(ref),
				"a fetchable address must not be looked up as a local profile name")
		})
	}

	localRefs := map[string]string{
		"bare name":          "developer",
		"ctxloom:local":      "ctxloom:local@bundles/dev",
		"canonical local":    "ctxloom+local:bundles/dev",
		"canonical builtin":  "ctxloom+builtin:core",
		"canonicalcompanion": "ctxloom+companion:ltk",
	}
	for name, ref := range localRefs {
		t.Run(name, func(t *testing.T) {
			assert.False(t, isRemoteReference(ref),
				"nothing is fetched for this class, so it must be resolved locally")
		})
	}
}

// loaderWith returns a real profiles.Loader over memfs holding exactly these
// LOCAL profile names. requireProfilesExist takes the concrete loader (the
// profiles port was retired), so the double is a real loader over a fake
// filesystem rather than a fake loader.
func loaderWith(t *testing.T, names ...string) *profiles.Loader {
	t.Helper()
	fs := afero.NewMemMapFs()
	dir := "/app/" + paths.AppDirName + "/profiles"
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	for _, n := range names {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, n+".yaml"), []byte("description: seeded\n"), 0o644))
	}
	return profiles.NewLoader([]string{dir}, profiles.WithFS(fs))
}

// TestRequireProfilesExist_CanonicalParentIsNotLookedUpLocally is the
// user-visible half: `profile create --parent <ref>` runs this check, and a ref
// it fails to recognize as remote is reported as `parent profile %q not found`
// — naming the one place the profile was never going to be.
func TestRequireProfilesExist_CanonicalParentIsNotLookedUpLocally(t *testing.T) {
	src := loaderWith(t, "local-parent")

	t.Run("canonical remote parent passes without a local lookup", func(t *testing.T) {
		err := requireProfilesExist(src, []string{
			"ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/ai-developer#profiles/developer",
		})
		assert.NoError(t, err, "a remote parent is fetched, not resolved against local profile names")
	})

	t.Run("retired spelling still passes", func(t *testing.T) {
		err := requireProfilesExist(src, []string{
			"https://github.com/ctxloom/ctxloom-default@bundles/ai-developer#profiles/developer",
		})
		assert.NoError(t, err, "the retired spelling a user may still have typed must keep working")
	})

	// The control: the check still REFUSES a local name that does not exist,
	// so the two cases above are not passing because the check stopped checking.
	t.Run("a missing local parent is still refused", func(t *testing.T) {
		assert.Error(t, requireProfilesExist(src, []string{"no-such-parent"}),
			"a bare name is a local profile and must still be verified to exist")
		assert.NoError(t, requireProfilesExist(src, []string{"local-parent"}),
			"and an existing one still passes")
	})
}
