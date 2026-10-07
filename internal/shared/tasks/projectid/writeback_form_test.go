package projectid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/yamlform"
)

// TestRegistrySaveIsWriteBackForm: the project registry is saved in the
// encoding an upgrade write-back would give it.
func TestRegistrySaveIsWriteBackForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.yaml")
	m, err := Open(path)
	require.NoError(t, err)
	_, err = m.Mint(t.TempDir())
	require.NoError(t, err)

	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	yamlform.RequireWriteBackForm(t, saved)
}
