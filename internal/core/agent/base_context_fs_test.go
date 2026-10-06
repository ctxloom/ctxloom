package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The provider writes and removes its context file through its afero.Fs, so a
// decorator on that fs sees both halves. Driven on a MemMapFs at a work dir
// that exists on no real disk: a raw os call would fail on the missing
// directory or leave the file behind on disk.
func TestBaseContextProvider_ProvidesAndClearsThroughItsFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	work := filepath.Join(t.TempDir(), "never-on-disk")
	p := &BaseContextProvider{fs: mem}

	require.NoError(t, p.Provide(work, []*Fragment{{Content: "project rules"}}))
	path := filepath.Join(work, p.GetContextFilePath())
	exists, err := afero.Exists(mem, path)
	require.NoError(t, err)
	require.True(t, exists, "Provide must write the context file into the provider's fs")

	require.NoError(t, p.Clear(work))
	exists, err = afero.Exists(mem, path)
	require.NoError(t, err)
	assert.False(t, exists, "Clear must remove the context file from the provider's fs")
	assert.Empty(t, p.GetContextHash())

	_, err = os.Stat(work)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}
