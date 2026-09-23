package remote

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The scp spelling ("git@host:path") and the https spelling of one repository
// name one bundle, so they render one canonical identity — the one trust
// records, signatures and seeded profiles key on — and neither needs the
// fetch-address fallback to get there.
func TestCanonicalString_SSHAndHTTPSSpellingsOfOneRepoAreOneIdentity(t *testing.T) {
	var diag bytes.Buffer
	restore := clidiag.SetSink(&diag)
	defer restore()

	const want = "ctxloom+git://github.com/owner/repo//bundles/kit"
	for _, spelling := range []string{
		"https://github.com/owner/repo@bundles/kit",
		"git@github.com:owner/repo@bundles/kit",
	} {
		ref, err := ParseReference(spelling)
		require.NoError(t, err, spelling)
		assert.Equal(t, want, ref.CanonicalString(), spelling)

		key, err := CanonicalBundleRef(spelling)
		require.NoError(t, err, spelling)
		assert.Equal(t, want, key, spelling)
	}
	assert.NotContains(t, diag.String(), "cannot render", "no spelling of a real repository falls back to its fetch address")
}

// The fold keeps what the https spelling keeps: a ".git" suffix is part of the
// identity on both, so the two spellings still agree.
func TestCanonicalString_SSHAndHTTPSAgreeWithGitSuffix(t *testing.T) {
	https, err := ParseReference("https://github.com/owner/repo.git@bundles/kit")
	require.NoError(t, err)
	ssh, err := ParseReference("git@github.com:owner/repo.git@bundles/kit")
	require.NoError(t, err)
	assert.Equal(t, https.CanonicalString(), ssh.CanonicalString())
}
