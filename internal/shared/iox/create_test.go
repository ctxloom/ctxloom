package iox

import (
	"io"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// TestCreate_GivesTheModeTheFilesystemsOwnCreateGives pins that Create is
// the filesystem's own Create and not a reimplementation of it: MemMapFs's
// Create leaves a new file with no permission bits, whereas an OpenFile with
// an explicit perm chmods the file to it. A copy-up through an overlay layer
// lands with the mode the layer holds, so a Create that picked a perm would
// change every copied-up file's mode.
func TestCreate_GivesTheModeTheFilesystemsOwnCreateGives(t *testing.T) {
	reference := afero.NewMemMapFs()
	want, err := reference.Create("/f")
	require.NoError(t, err)
	wantInfo, err := want.Stat()
	require.NoError(t, err)

	fs := afero.NewMemMapFs()
	got, err := Create(fs, "/f")
	require.NoError(t, err)
	gotInfo, err := got.Stat()
	require.NoError(t, err)

	require.Equal(t, wantInfo.Mode(), gotInfo.Mode())
}

// TestCreate_TruncatesAnExistingFileAndOpensItForReadAndWrite pins the
// rest of Create's contract: an existing file is emptied, and the handle
// both writes and reads back.
func TestCreate_TruncatesAnExistingFileAndOpensItForReadAndWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, WriteFileAtomicFs(fs, "/f", []byte("previous"), 0o644))

	f, err := Create(fs, "/f")
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	_, err = f.WriteString("new")
	require.NoError(t, err)
	_, err = f.Seek(0, io.SeekStart)
	require.NoError(t, err)
	back, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "new", string(back))
}
