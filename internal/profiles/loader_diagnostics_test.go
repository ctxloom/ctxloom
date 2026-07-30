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
