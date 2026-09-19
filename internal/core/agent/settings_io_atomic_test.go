package agent

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingFs wraps an afero.Fs, recording every path opened for creation and
// optionally failing every Rename — the two behaviours the atomicity claim
// turns on (a unique rather than fixed temp name, and a rename failure
// surfacing as an error rather than silently degrading to a direct non-atomic
// overwrite that still returns nil).
type recordingFs struct {
	afero.Fs
	mu         sync.Mutex
	created    []string
	failRename error
}

func (r *recordingFs) Create(name string) (afero.File, error) {
	r.note(name)
	return r.Fs.Create(name)
}

func (r *recordingFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&os.O_CREATE != 0 {
		r.note(name)
	}
	return r.Fs.OpenFile(name, flag, perm)
}

func (r *recordingFs) Rename(oldname, newname string) error {
	if r.failRename != nil {
		return r.failRename
	}
	return r.Fs.Rename(oldname, newname)
}

func (r *recordingFs) note(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, name)
}

func (r *recordingFs) createdNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.created...)
}

// AtomicWriteFile must not use a FIXED temp name such as
// `path + ".ctxloom.tmp"` — precisely the concurrent-clobber hazard
// iox.WriteFileAtomic's unique name exists to prevent. Two writers to the same
// settings file sharing one temp path lets one truncate the other's in-flight
// bytes before either rename.
func TestAtomicWriteFile_TempNameIsUniqueNotFixed(t *testing.T) {
	fs := &recordingFs{Fs: afero.NewMemMapFs()}
	path := "/proj/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/proj/.claude", 0o755))

	require.NoError(t, AtomicWriteFile(fs, path, []byte(`{"a":1}`), "settings"))

	for _, name := range fs.createdNames() {
		assert.NotEqual(t, path+".ctxloom.tmp", name,
			"a fixed temp name lets a concurrent writer clobber an in-flight temp")
	}

	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(got))

	// No temp survives a successful write.
	entries, err := afero.ReadDir(fs, "/proj/.claude")
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.Contains(e.Name(), ".tmp"), "stray temp %q", e.Name())
	}
}

// A failed rename must be an error, never a fall-back to a DIRECT, non-atomic
// overwrite of the live settings file that returns nil — a reader could then
// observe a half-written settings.json on a path the caller was told had
// succeeded. A cross-device rename, the only thing that would justify such a
// fallback, cannot happen here: the temp lives in the destination directory.
func TestAtomicWriteFile_RenameFailureIsAnError(t *testing.T) {
	fs := &recordingFs{Fs: afero.NewMemMapFs(), failRename: errors.New("rename refused")}
	path := "/proj/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/proj/.claude", 0o755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(`{"old":true}`), 0o600))

	err := AtomicWriteFile(fs, path, []byte(`{"new":true}`), "settings")
	require.Error(t, err, "a rename failure must not be reported as success")

	// The live file is untouched, not half-overwritten.
	got, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.Equal(t, `{"old":true}`, string(got))
}

// The surrounding contract AtomicWriteFile owns: mode preservation, the 0600
// default for new files, the ABSENCE of a backup sibling, and the zero-byte refusal
// guard.
func TestAtomicWriteFile_ContractPreserved(t *testing.T) {
	t.Run("new file defaults to 0600", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/p", 0o755))
		require.NoError(t, AtomicWriteFile(fs, "/p/s.json", []byte("x"), "settings"))
		info, err := fs.Stat("/p/s.json")
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})

	t.Run("existing mode preserved and no backup written", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/p", 0o755))
		require.NoError(t, afero.WriteFile(fs, "/p/s.json", []byte("old"), 0o640))
		require.NoError(t, AtomicWriteFile(fs, "/p/s.json", []byte("new"), "settings"))

		info, err := fs.Stat("/p/s.json")
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())

		bakExists, err := afero.Exists(fs, "/p/s.json.ctxloom.bak")
		require.NoError(t, err)
		assert.False(t, bakExists, "the single-slot backup sibling was retired with the wholesale-rewrite behaviour that needed it")
	})

	t.Run("zero bytes over an existing file is refused", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/p", 0o755))
		require.NoError(t, afero.WriteFile(fs, "/p/s.json", []byte("live"), 0o600))
		err := AtomicWriteFile(fs, "/p/s.json", nil, "settings")
		require.Error(t, err)
		got, rerr := afero.ReadFile(fs, "/p/s.json")
		require.NoError(t, rerr)
		assert.Equal(t, "live", string(got))
	})

	t.Run("AllowEmptyWrite opts out of the refusal", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/p", 0o755))
		require.NoError(t, afero.WriteFile(fs, "/p/c.toml", []byte("live"), 0o600))
		require.NoError(t, AtomicWriteFile(fs, "/p/c.toml", nil, "config", AllowEmptyWrite()))
		got, rerr := afero.ReadFile(fs, "/p/c.toml")
		require.NoError(t, rerr)
		assert.Empty(t, string(got))
	})
}
