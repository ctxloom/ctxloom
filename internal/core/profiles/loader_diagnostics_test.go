package profiles

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLoad_RemoteSchemeRefsReportNoLockfileEntry pins the SEAM above the
// scheme test Load uses to tell a remote profile reference from a local
// profile name: every canonical URL scheme must produce the "no lockfile
// entry" diagnosis (a remote ref can never resolve from disk), and a bare
// local name must not. The scheme list itself is remote.IsFetchAddressRef's;
// this pin is what keeps a second, drifting copy of it out of this package.
func TestLoad_RemoteSchemeRefsReportNoLockfileEntry(t *testing.T) {
	loader := bundleLoader(t, afero.NewMemMapFs())

	for _, ref := range []string{
		"https://github.com/owner/repo@bundles/b",
		"http://example.com/owner/repo@bundles/b",
		"git@github.com:owner/repo.git@bundles/b",
		"file:///tmp/repo@bundles/b",
	} {
		_, err := loader.Load(ref)
		require.Error(t, err, "ref %q", ref)
		require.ErrorIs(t, err, errs.ErrProfileNotFound, "ref %q", ref)
		assert.Contains(t, err.Error(), "no lockfile entry", "ref %q", ref)
	}

	// A bare local name must NOT take the remote arm: it is looked up on disk
	// and reports a plain not-found.
	_, err := loader.Load("go-developer")
	require.ErrorIs(t, err, errs.ErrProfileNotFound)
	assert.NotContains(t, err.Error(), "no lockfile entry")
}

// TestSave_NewProfileNeverOverwritesAFileThatDidNotLoad pins the hazard behind
// "does this name exist?": CreateProfile treats an unknown name as free and
// Save then writes a new item. A profile file that is present but did not load
// — malformed, so it never reached the seed — is not free, and writing a new
// profile over it would silently replace the user's file.
func TestSave_NewProfileNeverOverwritesAFileThatDidNotLoad(t *testing.T) {
	const broken = "bundles: [unclosed\n"
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testProfilesDir, 0o755))
	loader := bundleLoader(t, fs)
	testsupport.WriteFileString(t, fs, testProfilesDir+"/broken.yaml", broken, 0o644)

	assert.False(t, loader.Exists("broken"), "a profile that did not load is not resolvable")
	err := loader.Save(&Profile{Name: "broken", Bundles: []string{"go-development"}})
	require.ErrorIs(t, err, os.ErrExist)

	after, err := afero.ReadFile(fs, testProfilesDir+"/broken.yaml")
	require.NoError(t, err)
	assert.Equal(t, broken, string(after), "the user's file is untouched")

	// Names that can never address a profile stay false.
	assert.False(t, loader.Exists("absent"))
	assert.False(t, loader.Exists("../escape"))
	assert.False(t, loader.Exists("some/bundle#profiles/p"))
	assert.False(t, loader.Exists("https://github.com/owner/repo@bundles/b"))
}

// TestExists_SeededProfile pins that a seeded (bundle-shipped) profile exists
// through the same accessor, since it has no file behind it at all.
func TestExists_SeededProfile(t *testing.T) {
	loader, p, _ := seedTestProfile(t)
	assert.True(t, loader.Exists(p.Name))
}
