package operations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// lockKeyOf is the lockfile key a pull of ref writes: its bundle identity
// (remote.Reference.LockKey). Tests spell a dependency the way a user types
// it and key the lockfile through this, so a fixture can never hold an entry
// under a spelling no production lookup reaches.
func lockKeyOf(t testing.TB, ref string) trust.BundleKey {
	t.Helper()
	parsed, err := remote.ParseReference(ref)
	require.NoError(t, err, "ref %q", ref)
	key, err := parsed.LockKey()
	require.NoError(t, err, "ref %q", ref)
	return key
}
