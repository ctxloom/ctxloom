package iox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAtomicFile_VisibleOnlyAfterCommit pins the whole reason AtomicFile
// exists: bytes handed to Write must not appear at path until Commit renames
// the temp file into place. A version that renamed eagerly (e.g. inside
// Write) would let a reader observe a partial file mid-stream — this goes red
// against that mutation.
func TestAtomicFile_VisibleOnlyAfterCommit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")

	af, err := NewAtomicFile(target, 0o644)
	require.NoError(t, err)

	_, err = af.Write([]byte("line one\n"))
	require.NoError(t, err)
	_, err = af.Write([]byte("line two\n"))
	require.NoError(t, err)

	// Nothing at target yet.
	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr), "target must not exist before Commit")

	require.NoError(t, af.Commit())

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "line one\nline two\n", string(got))

	// No temp file survives a successful commit.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a temp file was left behind")
	assert.Equal(t, "out.jsonl", entries[0].Name())
}

// TestAtomicFile_TempNameIsUnique pins the same concurrent-clobber protection
// WriteFileAtomicFs provides: the temp name is derived from the target's base
// name plus randomness, not a fixed "<path>.tmp" two writers could collide on.
func TestAtomicFile_TempNameIsUnique(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")

	af, err := NewAtomicFile(target, 0o644)
	require.NoError(t, err)
	tmp := af.TempPath()

	assert.NotEqual(t, target+".tmp", tmp)
	assert.True(t, strings.HasPrefix(filepath.Base(tmp), ".out.jsonl."), "temp name %q must derive from the target base name", tmp)
	require.NoError(t, af.Commit())
}

// TestAtomicFile_Abort_LeavesNoTemp pins Abort's cleanup contract and that it
// never touches path.
func TestAtomicFile_Abort_LeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o644))

	af, err := NewAtomicFile(target, 0o644)
	require.NoError(t, err)
	_, err = af.Write([]byte("scratch"))
	require.NoError(t, err)

	require.NoError(t, af.Abort())

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "original", string(got), "Abort must never touch the destination")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "Abort left a temp file behind")
	assert.Equal(t, "out.jsonl", entries[0].Name())
}

// TestAtomicFile_TempPath_ExternalWritesAreCommitted pins the escape hatch
// TempPath exists for: a caller that writes to the temp path directly (never
// calling af.Write at all) still gets those bytes installed on Commit,
// because Commit stats the file on disk rather than trusting an internal
// Write-call counter.
func TestAtomicFile_TempPath_ExternalWritesAreCommitted(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")

	af, err := NewAtomicFile(target, 0o644)
	require.NoError(t, err)

	f, err := os.OpenFile(af.TempPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("external bytes\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, af.Commit())

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "external bytes\n", string(got))
}

// TestAtomicFile_EmptyCommitOverExisting_Refused pins the empty-guard applied
// at Commit: a zero-byte result over an existing file is a bug upstream by
// default, refused rather than silently truncating the live file.
func TestAtomicFile_EmptyCommitOverExisting_Refused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("live"), 0o644))

	af, err := NewAtomicFile(target, 0o644)
	require.NoError(t, err)

	err = af.Commit()
	require.Error(t, err)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "live", string(got), "a refused commit must leave the original bytes untouched")
}

// TestAtomicFile_EmptyCommitOverExisting_AllowEmpty pins the opt-out.
func TestAtomicFile_EmptyCommitOverExisting_AllowEmpty(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("live"), 0o644))

	af, err := NewAtomicFile(target, 0o644, AllowEmpty())
	require.NoError(t, err)

	require.NoError(t, af.Commit())

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Empty(t, string(got))
}

// TestAtomicFile_EmptyCommitToNewPath_Proceeds pins the same "new path is not
// a truncation" rule the whole-file entry points apply.
func TestAtomicFile_EmptyCommitToNewPath_Proceeds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")

	af, err := NewAtomicFile(target, 0o644)
	require.NoError(t, err)
	require.NoError(t, af.Commit())

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Empty(t, string(got))
}

// TestAtomicFile_DoubleCommit_Refused and its Write/Abort siblings pin the
// single-use lifecycle: a caller confused about which state the AtomicFile is
// in must get an error, never a silent no-op or a second rename.
func TestAtomicFile_DoubleCommit_Refused(t *testing.T) {
	dir := t.TempDir()
	af, err := NewAtomicFile(filepath.Join(dir, "out.jsonl"), 0o644)
	require.NoError(t, err)
	_, _ = af.Write([]byte("x"))
	require.NoError(t, af.Commit())
	assert.Error(t, af.Commit())
}

func TestAtomicFile_WriteAfterCommit_Refused(t *testing.T) {
	dir := t.TempDir()
	af, err := NewAtomicFile(filepath.Join(dir, "out.jsonl"), 0o644)
	require.NoError(t, err)
	_, _ = af.Write([]byte("x"))
	require.NoError(t, af.Commit())
	_, err = af.Write([]byte("y"))
	assert.Error(t, err)
}

func TestAtomicFile_CommitAfterAbort_Refused(t *testing.T) {
	dir := t.TempDir()
	af, err := NewAtomicFile(filepath.Join(dir, "out.jsonl"), 0o644)
	require.NoError(t, err)
	require.NoError(t, af.Abort())
	assert.Error(t, af.Commit())
}

func TestAtomicFile_DoubleAbort_Refused(t *testing.T) {
	dir := t.TempDir()
	af, err := NewAtomicFile(filepath.Join(dir, "out.jsonl"), 0o644)
	require.NoError(t, err)
	require.NoError(t, af.Abort())
	assert.Error(t, af.Abort())
}

// TestAtomicFile_ExactPerm pins the exact-chmod contract (not umask-masked),
// matching WriteFileAtomicFs.
func TestAtomicFile_ExactPerm(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.jsonl")

	af, err := NewAtomicFile(target, 0o600)
	require.NoError(t, err)
	_, err = af.Write([]byte("secret"))
	require.NoError(t, err)
	require.NoError(t, af.Commit())

	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
