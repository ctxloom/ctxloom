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
// default for new files, the .ctxloom.bak backup, and the zero-byte refusal
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

	t.Run("existing mode preserved and backed up", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/p", 0o755))
		require.NoError(t, afero.WriteFile(fs, "/p/s.json", []byte("old"), 0o640))
		require.NoError(t, AtomicWriteFile(fs, "/p/s.json", []byte("new"), "settings"))

		info, err := fs.Stat("/p/s.json")
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())

		bak, err := afero.ReadFile(fs, "/p/s.json.ctxloom.bak")
		require.NoError(t, err)
		assert.Equal(t, "old", string(bak))
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

// backupHostileFs fails exactly one filesystem operation on exactly one path,
// so a test can break the `.ctxloom.bak` backup without disturbing either the
// temp file iox writes or the destination rename.
type backupHostileFs struct {
	afero.Fs
	failOpenFileOn string // OpenFile on this path returns failErr
	failOpenOn     string // Open (afero.ReadFile) on this path returns failErr
	failErr        error
}

func (b *backupHostileFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if b.failOpenFileOn != "" && name == b.failOpenFileOn {
		return nil, b.failErr
	}
	return b.Fs.OpenFile(name, flag, perm)
}

func (b *backupHostileFs) Open(name string) (afero.File, error) {
	if b.failOpenOn != "" && name == b.failOpenOn {
		return nil, b.failErr
	}
	return b.Fs.Open(name)
}

// A settings file is only safely overwritable because the previous bytes were
// captured to <path>.ctxloom.bak first — that backup IS the recovery path
// AtomicWriteFile's atomicity claim leans on for a bad-but-well-formed write.
// So a backup that could not be taken must abort the write, exactly as the
// zero-byte guard does: proceeding leaves the user with no way back and no way
// to know. Both halves count — the read of the original and the write of the
// copy — because either one failing means no backup exists.
func TestAtomicWriteFile_BackupFailureAbortsTheWrite(t *testing.T) {
	const path = "/proj/.claude/settings.json"
	boom := errors.New("backup medium refused")

	t.Run("the backup copy cannot be written", func(t *testing.T) {
		fs := &backupHostileFs{Fs: afero.NewMemMapFs(), failOpenFileOn: path + ".ctxloom.bak", failErr: boom}
		require.NoError(t, fs.MkdirAll("/proj/.claude", 0o755))
		require.NoError(t, afero.WriteFile(fs, path, []byte(`{"old":true}`), 0o600))

		err := AtomicWriteFile(fs, path, []byte(`{"new":true}`), "settings")
		require.Error(t, err, "a settings overwrite with no recoverable backup must not report success")
		assert.ErrorIs(t, err, boom, "the swallowed cause must reach the caller")

		got, rerr := afero.ReadFile(fs, path)
		require.NoError(t, rerr)
		assert.Equal(t, `{"old":true}`, string(got), "the original must survive an aborted write")
	})

	t.Run("the original cannot be read", func(t *testing.T) {
		fs := &backupHostileFs{Fs: afero.NewMemMapFs(), failOpenOn: path, failErr: boom}
		require.NoError(t, fs.MkdirAll("/proj/.claude", 0o755))
		require.NoError(t, afero.WriteFile(fs, path, []byte(`{"old":true}`), 0o600))

		err := AtomicWriteFile(fs, path, []byte(`{"new":true}`), "settings")
		require.Error(t, err, "an unreadable original is the case where losing it is least recoverable")
		assert.ErrorIs(t, err, boom)
	})

	t.Run("a brand-new file has nothing to back up and still writes", func(t *testing.T) {
		fs := &backupHostileFs{Fs: afero.NewMemMapFs(), failOpenFileOn: path + ".ctxloom.bak", failErr: boom}
		require.NoError(t, fs.MkdirAll("/proj/.claude", 0o755))

		require.NoError(t, AtomicWriteFile(fs, path, []byte(`{"new":true}`), "settings"),
			"no existing file means no backup is owed; the guard must not block first writes")
		got, rerr := afero.ReadFile(fs, path)
		require.NoError(t, rerr)
		assert.Equal(t, `{"new":true}`, string(got))
	})
}
