package iox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A regular file is refused: the helper can never be used to write one.
func TestOpenFIFOWriter_RefusesAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(p, nil, 0o600))
	_, err := OpenFIFOWriter(p)
	assert.ErrorIs(t, err, ErrNotAFIFO)
	_, err = OpenFIFOWriter(filepath.Join(t.TempDir(), "absent"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
