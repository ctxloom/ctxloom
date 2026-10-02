package safefs_test

import (
	"io"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

const guarded = "/d/live.txt"

func seeded(t *testing.T) afero.Fs {
	t.Helper()
	base := afero.NewMemMapFs()
	require.NoError(t, base.MkdirAll("/d", 0o755))
	require.NoError(t, afero.WriteFile(base, guarded, []byte("keep me"), 0o644))
	return base
}

func content(t *testing.T, fs afero.Fs, path string) string {
	t.Helper()
	b, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	return string(b)
}

// The forcing case for the whole decorator: an empty file renamed over a live
// one is the shape of every atomic write that produced nothing. Without the
// guard the base fs performs it and the live bytes are gone.
func TestGuardFs_RefusesEmptyRenameOverExisting(t *testing.T) {
	base := seeded(t)
	require.NoError(t, afero.WriteFile(base, "/d/empty.tmp", nil, 0o644))

	err := safefs.NewGuardFs(base).Rename("/d/empty.tmp", guarded)

	require.ErrorIs(t, err, safefs.ErrEmptyOverwrite)
	assert.Equal(t, "keep me", content(t, base, guarded))
}

// Proves the test above measures the guard and not the base: the same rename
// on the bare fs succeeds and destroys the content.
func TestGuardFs_BaseAloneLosesTheContent(t *testing.T) {
	base := seeded(t)
	require.NoError(t, afero.WriteFile(base, "/d/empty.tmp", nil, 0o644))

	require.NoError(t, base.Rename("/d/empty.tmp", guarded))
	assert.Empty(t, content(t, base, guarded))
}

func TestGuardFs_EmptyRenameToNewPathProceeds(t *testing.T) {
	base := seeded(t)
	require.NoError(t, afero.WriteFile(base, "/d/empty.tmp", nil, 0o644))

	require.NoError(t, safefs.NewGuardFs(base).Rename("/d/empty.tmp", "/d/new.txt"))
	assert.Empty(t, content(t, base, "/d/new.txt"))
}

func TestGuardFs_NonEmptyRenameOverExistingProceeds(t *testing.T) {
	base := seeded(t)
	require.NoError(t, afero.WriteFile(base, "/d/full.tmp", []byte("x"), 0o644))

	require.NoError(t, safefs.NewGuardFs(base).Rename("/d/full.tmp", guarded))
	assert.Equal(t, "x", content(t, base, guarded))
}

// A directory has no byte length worth guarding; renaming one is not a
// truncation even when Stat reports size zero.
func TestGuardFs_DirectoryRenameIsNotJudged(t *testing.T) {
	base := seeded(t)
	require.NoError(t, base.MkdirAll("/d/sub", 0o755))
	require.NoError(t, base.MkdirAll("/e", 0o755))

	require.NoError(t, safefs.NewGuardFs(base).Rename("/d/sub", "/e/sub"))
}

func TestGuardFs_TruncatingOpenClosedUnwrittenKeepsContent(t *testing.T) {
	for name, open := range map[string]func(afero.Fs) (afero.File, error){
		"Create":   func(fs afero.Fs) (afero.File, error) { return fs.Create(guarded) },
		"OpenFile": func(fs afero.Fs) (afero.File, error) { return fs.OpenFile(guarded, os.O_WRONLY|os.O_TRUNC, 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			base := seeded(t)
			f, err := open(safefs.NewGuardFs(base))
			require.NoError(t, err)

			require.ErrorIs(t, f.Close(), safefs.ErrEmptyOverwrite)
			assert.Equal(t, "keep me", content(t, base, guarded))
		})
	}
}

// A zero-length Write is not a write: counting it would let `f.Write(nil)`
// defeat the guard.
func TestGuardFs_ZeroLengthWriteDoesNotArmTheTruncate(t *testing.T) {
	base := seeded(t)
	f, err := safefs.NewGuardFs(base).OpenFile(guarded, os.O_WRONLY|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	n, err := f.Write(nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = f.WriteAt(nil, 0)
	require.NoError(t, err)
	assert.Zero(t, n)

	require.ErrorIs(t, f.Close(), safefs.ErrEmptyOverwrite)
	assert.Equal(t, "keep me", content(t, base, guarded))
}

func TestGuardFs_TruncatingOpenThenWriteReplaces(t *testing.T) {
	writes := map[string]func(afero.File) error{
		"Write":       func(f afero.File) error { _, err := f.Write([]byte("new")); return err },
		"WriteString": func(f afero.File) error { _, err := f.WriteString("new"); return err },
		"WriteAt":     func(f afero.File) error { _, err := f.WriteAt([]byte("new"), 0); return err },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			base := seeded(t)
			f, err := safefs.NewGuardFs(base).Create(guarded)
			require.NoError(t, err)
			require.NoError(t, write(f))
			require.NoError(t, f.Close())
			assert.Equal(t, "new", content(t, base, guarded))
		})
	}
}

// An explicit Truncate is the caller saying what size it wants; it is not a
// silent no-op.
func TestGuardFs_ExplicitTruncateIsHonoured(t *testing.T) {
	base := seeded(t)
	f, err := safefs.NewGuardFs(base).Create(guarded)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(0))
	require.NoError(t, f.Close())
	assert.Empty(t, content(t, base, guarded))
}

// While the truncation is pending the file must read as the empty file the
// caller asked for, not the bytes it is about to replace.
func TestGuardFs_PendingTruncateReadsEmpty(t *testing.T) {
	base := seeded(t)
	f, err := safefs.NewGuardFs(base).OpenFile(guarded, os.O_RDWR|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	n, err := f.Read(make([]byte, 4))
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF)
	_, err = f.ReadAt(make([]byte, 4), 0)
	assert.ErrorIs(t, err, io.EOF)
	info, err := f.Stat()
	require.NoError(t, err)
	assert.Zero(t, info.Size())
	end, err := f.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	assert.Zero(t, end)
}

func TestGuardFs_CreateOfNewPathIsUntouched(t *testing.T) {
	base := seeded(t)
	f, err := safefs.NewGuardFs(base).Create("/d/new.txt")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Empty(t, content(t, base, "/d/new.txt"))
}

// A brand-new file has nothing to lose: creating it empty through a
// truncating open is not an overwrite.
func TestGuardFs_TruncatingCreateOfNewPathClosesClean(t *testing.T) {
	base := seeded(t)
	f, err := safefs.NewGuardFs(base).OpenFile("/d/new.txt", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Empty(t, content(t, base, "/d/new.txt"))
}

func TestGuardFs_NonTruncatingOpenIsUntouched(t *testing.T) {
	base := seeded(t)
	f, err := safefs.NewGuardFs(base).OpenFile(guarded, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, "keep me", content(t, base, guarded))
}

func TestGuardFs_IsIdempotent(t *testing.T) {
	g := safefs.NewGuardFs(afero.NewMemMapFs())
	assert.Same(t, g, safefs.NewGuardFs(g))
}

// AllowEmpty is the caller's explicit decision, so it overrides a guard the
// caller's fs already carries.
func TestWriteFile_AllowEmptyStripsACallersGuard(t *testing.T) {
	base := seeded(t)
	require.NoError(t, safefs.WriteFile(safefs.NewGuardFs(base), guarded, nil, 0o644, safefs.AllowEmpty()))
	assert.Empty(t, content(t, base, guarded))
}

func TestWriteFile_GuardedByDefault(t *testing.T) {
	base := seeded(t)
	err := safefs.WriteFile(base, guarded, nil, 0o644)
	require.ErrorIs(t, err, safefs.ErrEmptyOverwrite)
	assert.Equal(t, "keep me", content(t, base, guarded))
}
