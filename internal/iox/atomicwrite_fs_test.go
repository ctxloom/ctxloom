package iox

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomicFs_WritesContentAndCleansTemp(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/dir", 0o755))

	require.NoError(t, WriteFileAtomicFs(fs, "/dir/out.yaml", []byte("hello"), 0o644))

	data, err := afero.ReadFile(fs, "/dir/out.yaml")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))

	infos, err := afero.ReadDir(fs, "/dir")
	require.NoError(t, err)
	for _, fi := range infos {
		assert.False(t, strings.HasSuffix(fi.Name(), ".tmp"), "leftover temp: %s", fi.Name())
	}
}

func TestWriteFileAtomicFs_OverwritesExisting(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/dir", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/dir/out.yaml", []byte("old"), 0o644))

	require.NoError(t, WriteFileAtomicFs(fs, "/dir/out.yaml", []byte("new"), 0o644))

	data, err := afero.ReadFile(fs, "/dir/out.yaml")
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
}
