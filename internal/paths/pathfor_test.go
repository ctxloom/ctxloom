package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// PathFor owns the one thing its callers cannot get wrong quietly. Two
// writers of the same resource that name the lock file differently do not
// exclude each other, and nothing reports it: no error, no warning, just two
// writers where there was meant to be one.
func TestPathFor_DerivesOneLockPerResource(t *testing.T) {
	protected := filepath.Join(t.TempDir(), "index.json")

	require.Equal(t, PathFor(protected), PathFor(protected),
		"the same resource must always derive the same lock")
	require.NotEqual(t, protected, PathFor(protected),
		"the lock must not be the protected file itself")
	require.NotEqual(t, PathFor(protected), PathFor(protected+"2"),
		"two resources must not collide onto one lock")
}
