package iox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// faultFs fails exactly one step of the atomic-write sequence so each of the
// six error returns can be observed on its own. Everything else delegates to a
// real MemMapFs, so the sequence reaches the step under test for real rather
// than being short-circuited.
type faultFs struct {
	afero.Fs
	failChmod  bool
	failRename bool
	failWrite  bool
	failSync   bool
	failClose  bool
}

var errInjected = errors.New("injected failure")

func (f *faultFs) Chmod(name string, mode os.FileMode) error {
	if f.failChmod {
		return errInjected
	}
	return f.Fs.Chmod(name, mode)
}

func (f *faultFs) Rename(oldname, newname string) error {
	if f.failRename {
		return errInjected
	}
	return f.Fs.Rename(oldname, newname)
}

func (f *faultFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil || (!f.failWrite && !f.failSync && !f.failClose) {
		return file, err
	}
	return &faultFile{File: file, fs: f}, nil
}

type faultFile struct {
	afero.File
	fs *faultFs
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.fs.failWrite {
		return 0, errInjected
	}
	return f.File.Write(p)
}

func (f *faultFile) Sync() error {
	if f.fs.failSync {
		return errInjected
	}
	return f.File.Sync()
}

func (f *faultFile) Close() error {
	if f.fs.failClose {
		return errInjected
	}
	return f.File.Close()
}

// TestWriteFileAtomicFs_ErrorsNameTheTarget pins U113-F05's remedy at every one
// of the six error returns: whatever step fails, the caller must be told which
// FILE IT ASKED TO WRITE and which step failed. A bare stdlib error is the
// wrong answer twice over here — the most likely failure (creating the temp
// file) names the TEMP path, a name the caller never chose and cannot act on,
// and several callers propagate without wrapping, so nothing downstream adds
// the target either.
//
// Wrapping must stay transparent: errors.Is on the underlying cause is how a
// caller distinguishes a missing directory from a full disk, so each case
// asserts the cause survives.
func TestWriteFileAtomicFs_ErrorsNameTheTarget(t *testing.T) {
	target := filepath.Join("/data", "settings.json")

	cases := []struct {
		name string
		// setup builds the filesystem and returns the target to write to.
		setup func(t *testing.T) (afero.Fs, string)
		cause error
		step  string
	}{
		{
			name: "create temp",
			setup: func(t *testing.T) (afero.Fs, string) {
				// A parent directory that does not exist: the failure the row
				// calls out as the most likely one.
				return afero.NewReadOnlyFs(afero.NewMemMapFs()), target
			},
			cause: nil,
			step:  "temp file",
		},
		{
			name: "write",
			setup: func(t *testing.T) (afero.Fs, string) {
				return &faultFs{Fs: memWithDir(t), failWrite: true}, target
			},
			cause: errInjected,
			step:  "write",
		},
		{
			name: "sync",
			setup: func(t *testing.T) (afero.Fs, string) {
				return &faultFs{Fs: memWithDir(t), failSync: true}, target
			},
			cause: errInjected,
			step:  "sync",
		},
		{
			name: "close",
			setup: func(t *testing.T) (afero.Fs, string) {
				return &faultFs{Fs: memWithDir(t), failClose: true}, target
			},
			cause: errInjected,
			step:  "close",
		},
		{
			name: "chmod",
			setup: func(t *testing.T) (afero.Fs, string) {
				return &faultFs{Fs: memWithDir(t), failChmod: true}, target
			},
			cause: errInjected,
			step:  "mode",
		},
		{
			name: "rename",
			setup: func(t *testing.T) (afero.Fs, string) {
				return &faultFs{Fs: memWithDir(t), failRename: true}, target
			},
			cause: errInjected,
			step:  "rename",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys, path := tc.setup(t)

			err := WriteFileAtomicFs(fsys, path, []byte("payload"), 0o644)

			// Fixture check first: the step really did fail. A green assertion
			// on a write that succeeded would prove nothing.
			require.Error(t, err, "the injected fault did not make the write fail")

			assert.Contains(t, err.Error(), path,
				"the error must name the file the caller asked to write, not only the temp file")
			assert.Contains(t, strings.ToLower(err.Error()), tc.step,
				"the error must say which step failed")
			if tc.cause != nil {
				assert.ErrorIs(t, err, tc.cause, "wrapping must keep the cause inspectable")
			}
		})
	}
}

// TestWriteFileAtomicFs_MissingDirKeepsErrNotExist pins the one cause callers
// most plausibly branch on surviving the wrap.
func TestWriteFileAtomicFs_MissingDirKeepsErrNotExist(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nope", "settings.json")

	err := WriteFileAtomicFs(afero.NewOsFs(), target, []byte("x"), 0o644)
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Contains(t, err.Error(), target,
		"the exact target path, not the decorated temp name, is what the caller can act on")
}

func memWithDir(t *testing.T) afero.Fs {
	t.Helper()
	m := afero.NewMemMapFs()
	require.NoError(t, m.MkdirAll("/data", 0o755))
	return m
}
