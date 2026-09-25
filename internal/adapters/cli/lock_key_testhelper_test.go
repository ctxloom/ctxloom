package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// lockKeyOf is the lockfile key a pull of ref writes: its bundle identity.
func lockKeyOf(t testing.TB, ref string) trust.BundleKey {
	t.Helper()
	parsed, err := remote.ParseReference(ref)
	require.NoError(t, err, "ref %q", ref)
	key, err := parsed.LockKey()
	require.NoError(t, err, "ref %q", ref)
	return key
}
