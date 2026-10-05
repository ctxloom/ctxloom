package vendorreader

import (
	"io/fs"
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAndReadJSONLLines_ReadsThroughTheGivenFs: the lines come from the
// injected fs, so a file that exists only there is read, and nothing on disk
// is consulted.
func TestOpenAndReadJSONLLines_ReadsThroughTheGivenFs(t *testing.T) {
	const path = "/vendorreader-memfs-only/session.jsonl"
	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, fs.ErrNotExist, "the path must be absent from disk")

	mem := afero.NewMemMapFs()
	testsupport.WriteFile(t, mem, path, []byte("{\"a\":1}\n{\"b\":2}\n"), 0o644)

	lines, err := OpenAndReadJSONLLines(mem, "claude", path)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, `{"b":2}`, string(lines[1]))
}
