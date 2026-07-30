package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// TestLoad_RemoteSchemeRefsReportNoLockfileEntry pins the SEAM above the
// scheme test Load uses to tell a remote profile reference from a local
// profile name: every canonical URL scheme must produce the "no lockfile
// entry" diagnosis (a remote ref can never resolve from disk), and a bare
// local name must not. The scheme list itself is remote.IsCanonicalRef's;
// this pin is what keeps a second, drifting copy of it out of this package
// (U091-F04).
func TestLoad_RemoteSchemeRefsReportNoLockfileEntry(t *testing.T) {
	loader := NewLoader([]string{"/profiles"}, WithFS(afero.NewMemMapFs()))

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

// TestExists_ReportsPresenceNotLoadability pins that Exists answers "is there a
// profile under this name", not "does it currently parse". Answering the second
// question is destructive: CreateProfile treats Exists==false as "the name is
// free" and Save then writes over whatever is actually on disk, so a profile
// with a YAML syntax error was silently replaced by a brand-new one and the
// user's file was gone (U091-F06).
func TestExists_ReportsPresenceNotLoadability(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/broken.yaml", []byte("bundles: [unclosed\n"), 0o644))
	require.NoError(t, afero.WriteFile(fs, "/profiles/good.yaml", []byte("bundles:\n  - go-development\n"), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	// The broken profile genuinely does not load...
	_, err := loader.Load("broken")
	require.Error(t, err)
	require.NotErrorIs(t, err, errs.ErrProfileNotFound, "a malformed profile is present, not absent")

	// ...but it is unmistakably there.
	assert.True(t, loader.Exists("broken"), "a present-but-malformed profile must report as existing")
	assert.True(t, loader.Exists("good"))

	// Names with no file behind them stay false, including the shapes Load
	// refuses outright.
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
