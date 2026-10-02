//go:build !windows

package coord

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
)

// The state dir holds the journals and endpoint.json (the consumer
// credential): a dir loosened after it was made is tightened again the next
// time it is resolved.
func TestStateDirForProject_ALoosenedDirIsOwnerOnlyAgain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := stateDirForProject("proj-key")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o755))

	again, err := stateDirForProject("proj-key")
	require.NoError(t, err)
	require.NoError(t, owneronly.Check(again))
}
