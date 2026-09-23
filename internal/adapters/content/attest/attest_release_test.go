package attest

import (
	"os"
	"path"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/release"
)

func fixtureRel(t *testing.T) release.Release {
	t.Helper()
	return release.Release{Name: "code-quality", Version: semver.MustParse("1.0.0")}
}

// Attack (c), re-homing: a trusted publisher's whole signed tree, copied
// byte-for-byte under another bundle's path. Every hash matches and the
// signature verifies, so only the signed NAME can refuse it.
func TestVerifyBundle_ATreeSignedAsOneBundleServedAsAnotherIsTampered(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))

	src := path.Join(storeRoot, "code-quality")
	dst := path.Join(storeRoot, "impostor")
	require.NoError(t, afero.Walk(fsys, src, func(p string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		rel := p[len(src):]
		if info.IsDir() {
			return fsys.MkdirAll(dst+rel, 0o755)
		}
		data, err := afero.ReadFile(fsys, p)
		require.NoError(t, err)
		return afero.WriteFile(fsys, dst+rel, data, 0o644)
	}))
	moved, err := store.Open(ctx, "impostor")
	require.NoError(t, err)

	v, err := VerifyBundle(ctx, moved, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusTampered, v.Status)
	assert.False(t, v.OK())
	assert.Contains(t, v.Detail, `signed as "code-quality"`)
	assert.Contains(t, v.Detail, `served as "impostor"`)
}

func TestVerifyManifest_ASignedManifestNamesItsPublisherAndRelease(t *testing.T) {
	store, b, _ := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))
	raw, err := b.ReadFile(ctx, content.ManifestPath)
	require.NoError(t, err)
	sigs, err := b.BundleSignatures(ctx)
	require.NoError(t, err)

	m, v := VerifyManifest(raw, sigs, rootTrusting(publisher("pub@example.test", pub)), now)
	assert.Equal(t, StatusManifestSigned, v.Status)
	assert.Equal(t, "pub@example.test", v.Principal)
	assert.Equal(t, "code-quality", m.Release().Name)
	assert.Equal(t, "1.0.0", m.Release().Version.String())

	// Untrusted: unattested, and the release is still returned for display.
	_, v = VerifyManifest(raw, sigs, rootTrusting(), now)
	assert.Equal(t, StatusUnattested, v.Status)

	// One byte changed: the stored signature no longer covers it.
	edited := append([]byte(nil), raw...)
	edited[len(edited)-2] ^= 1
	_, v = VerifyManifest(edited, sigs, rootTrusting(publisher("pub@example.test", pub)), now)
	assert.NotEqual(t, StatusManifestSigned, v.Status)
	assert.False(t, v.OK())

	// Not a manifest at all: tampered, never unattested.
	_, v = VerifyManifest([]byte("hello\n"), sigs, rootTrusting(publisher("pub@example.test", pub)), now)
	assert.Equal(t, StatusTampered, v.Status)
}
