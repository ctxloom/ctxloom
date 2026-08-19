package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestLoaderReadKey_ResolvesTheExactKey proves ReadKey is Read's load-path
// counterpart for a caller already holding a trust.BundleKey.
func TestLoaderReadKey_ResolvesTheExactKey(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n"))

	want, err := trust.LocalRef("kit")
	require.NoError(t, err)

	read, ok := loader.ReadKey(want.BundleIdentity())
	require.True(t, ok)
	assert.Equal(t, "kit", read.DisplayName())
}

// TestLoaderReadKey_UnknownKeyMisses proves a key nothing was resolved under
// misses cleanly rather than matching some other bundle.
func TestLoaderReadKey_UnknownKeyMisses(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n"))

	ghost, err := trust.LocalRef("no-such-bundle")
	require.NoError(t, err)

	_, ok := loader.ReadKey(ghost.BundleIdentity())
	assert.False(t, ok)
}

// TestLoaderLoadKey_ResolvesTheBundle proves LoadKey returns the parsed
// bundle for an exact key.
func TestLoaderLoadKey_ResolvesTheBundle(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n"))

	want, err := trust.LocalRef("kit")
	require.NoError(t, err)

	b, err := loader.LoadKey(want.BundleIdentity())
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "1.0.0", b.Version)
}

// TestLoaderLoadKey_UnknownKeyErrorsNotFound proves LoadKey fails closed —
// with the SAME sentinel Load uses — rather than returning a nil bundle with
// no error, which callers would otherwise mistake for "no bundle, but no
// problem" (this project's characteristic silent-no-op shape).
func TestLoaderLoadKey_UnknownKeyErrorsNotFound(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n"))

	ghost, err := trust.LocalRef("no-such-bundle")
	require.NoError(t, err)

	b, err := loader.LoadKey(ghost.BundleIdentity())
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrBundleNotFound)
	assert.Nil(t, b)
}
