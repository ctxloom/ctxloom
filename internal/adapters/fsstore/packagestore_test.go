package fsstore

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
)

// TestPackageStore_PutStowsUnderTheSessionDir_GetRedeemsByLocation: the
// location is store-relative and names the session; a redeeming store needs
// no harp of its own.
func TestPackageStore_PutStowsUnderTheSessionDir_GetRedeemsByLocation(t *testing.T) {
	root := t.TempDir()
	b := []byte(`{"package":{}}`)
	digest := sha256.Sum256(b)
	loc, err := PackageStore{Root: root, Harp: "harp-1"}.Put(context.Background(), digest, b)
	require.NoError(t, err)
	require.False(t, filepath.IsAbs(loc), "a location is store-relative, never a host path")
	require.FileExists(t, filepath.Join(root, "harp-1", "persist", "package", filepath.Base(loc)))

	back, err := PackageStore{Root: root}.Get(context.Background(), loc)
	require.NoError(t, err)
	require.Equal(t, b, back)

	again, err := PackageStore{Root: root, Harp: "harp-1"}.Put(context.Background(), digest, b)
	require.NoError(t, err)
	require.Equal(t, loc, again, "content-addressed: the same bytes stow to the same location")
}

// TestPackageStore_Get_MissingIsNil_BadShapeIsRefused: a claim nothing
// stowed answers nil (the claim check refuses it as missing); a location
// that is not one this store issues is refused before any read.
func TestPackageStore_Get_MissingIsNil_BadShapeIsRefused(t *testing.T) {
	root := t.TempDir()
	missing, err := PackageStore{Root: root}.Get(context.Background(), "harp-1/persist/package/"+hexDigest("x"))
	require.NoError(t, err)
	require.Nil(t, missing)

	for _, loc := range []string{"../../etc/passwd", "harp-1/persist/package/../../x", "/abs/path", "harp-1/persist/other/" + hexDigest("x"), "harp-1/persist/package/notahex"} {
		_, err := PackageStore{Root: root}.Get(context.Background(), loc)
		require.ErrorIs(t, err, ErrBadClaimLocation, loc)
	}
}

// TestPackageStore_Put_NeedsASession: a store that carries is rooted at a
// session; a nameless one refuses rather than invent a location.
func TestPackageStore_Put_NeedsASession(t *testing.T) {
	_, err := PackageStore{Root: t.TempDir()}.Put(context.Background(), [32]byte{}, []byte("x"))
	require.Error(t, err)
}

// TestPackageStore_ThroughTheClaimCheck: the adapter behind the core's
// claim check carries and redeems the same package.
func TestPackageStore_ThroughTheClaimCheck(t *testing.T) {
	root := t.TempDir()
	pkg := composite.Package{Context: composite.Context{Text: "ctx"}}
	enc, err := composite.Encode(pkg)
	require.NoError(t, err)
	c, err := composite.ClaimCheck{Store: PackageStore{Root: root, Harp: "harp-1"}}.Carry(context.Background(), enc)
	require.NoError(t, err)
	back, err := composite.Open(context.Background(), composite.Inline{}, composite.ClaimCheck{Store: PackageStore{Root: root}}, c)
	require.NoError(t, err)
	require.Equal(t, pkg, back)

	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(c.Claim.Location)), []byte("tampered"), 0o644))
	_, err = composite.Open(context.Background(), composite.Inline{}, composite.ClaimCheck{Store: PackageStore{Root: root}}, c)
	require.ErrorIs(t, err, composite.ErrDigestMismatch)
}

func hexDigest(s string) string {
	d := sha256.Sum256([]byte(s))
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range d {
		out[i*2], out[i*2+1] = hexdigits[b>>4], hexdigits[b&0x0f]
	}
	return string(out)
}
