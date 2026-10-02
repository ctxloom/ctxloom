package taskstest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// TestWriteFile_CreatesNestedParentsOnARealBackedFs is the whole point of the
// helper, proved on a filesystem that actually enforces it. A MemMapFs
// auto-creates missing parent directories on write, which would make this
// assertion pass even with the MkdirAll call deleted — exactly the false
// green the fixture/production mismatch this package exists to close was
// built on (afero.WriteFile onto a MemMapFs, standing in for a production
// writer that MkdirAlls first). afero.NewOsFs, rooted at t.TempDir, enforces
// it for real: os.OpenFile fails with ENOENT when a parent is missing.
func TestWriteFile_CreatesNestedParentsOnARealBackedFs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "deeply", "nested", "dir", "file.txt")

	WriteFile(t, afero.NewOsFs(), path, []byte("hello"), 0o644)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
	info, err := os.Stat(filepath.Join(root, "deeply", "nested", "dir"))
	if err != nil || !info.IsDir() {
		t.Fatalf("parent directory was not created: %v", err)
	}
}

// TestWriteFile_DirModeMatchesFileMode pins dirPermFor's DECIDED rule: a
// 0600 file (the private-store convention, e.g.
// operations.appendAllowedSignersLine's allowed_signers write) pairs with a
// 0700 directory, and every other file mode pairs with 0755 — mirroring that
// existing pairing rather than adding a fourth mode spelling.
func TestWriteFile_DirModeMatchesFileMode(t *testing.T) {
	cases := []struct {
		name     string
		filePerm os.FileMode
		wantDir  os.FileMode
	}{
		{"private 0600 file gets a 0700 dir", 0o600, 0o700},
		{"ordinary 0644 file gets a 0755 dir", 0o644, 0o755},
		{"ordinary 0640 file gets a 0755 dir", 0o640, 0o755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "sub", "file.dat")
			WriteFile(t, afero.NewOsFs(), path, []byte("x"), tc.filePerm)

			info, err := os.Stat(filepath.Join(root, "sub"))
			if err != nil {
				t.Fatalf("Stat parent: %v", err)
			}
			if got := info.Mode().Perm(); got != tc.wantDir {
				t.Fatalf("parent dir mode = %#o, want %#o", got, tc.wantDir)
			}
		})
	}
}

// TestWriteFile_FailsTheTestOnMkdirAllError proves the fixture stops rather
// than half-writing: a fs whose MkdirAll always errors must report a
// failure. It drives writeFile (the fatalReporter-typed body) with a
// recorder standing in for t, rather than a real *testing.T, so the failure
// this test is proving does not also fail THIS test — testing.TB cannot be
// implemented outside the testing package, which is exactly why writeFile is
// split out over the narrower interface.
func TestWriteFile_FailsTheTestOnMkdirAllError(t *testing.T) {
	fs := mkdirFailFs{Fs: afero.NewMemMapFs(), err: errors.New("boom")}
	rec := &fatalRecorder{}
	writeFile(rec, fs, "/a/b/c.txt", []byte("x"), 0o644)
	if len(rec.msgs) == 0 {
		t.Fatalf("expected WriteFile to report a failure when MkdirAll errors")
	}
}

// TestWriteFile_FailsTheTestOnWriteError is the write-side twin of
// TestWriteFile_FailsTheTestOnMkdirAllError: safefs.WriteFile's last
// step is fs.Rename(tmpName, path), so a fs whose Rename always errors
// exercises the write failure path without needing a genuinely broken
// filesystem.
func TestWriteFile_FailsTheTestOnWriteError(t *testing.T) {
	fs := renameFailFs{Fs: afero.NewMemMapFs(), err: errors.New("boom")}
	rec := &fatalRecorder{}
	writeFile(rec, fs, "/a/b/c.txt", []byte("x"), 0o644)
	if len(rec.msgs) == 0 {
		t.Fatalf("expected WriteFile to report a failure when the atomic write errors")
	}
}

// TestWriteFileString_WritesTheStringAsBytes pins that WriteFileString is
// exactly WriteFile([]byte(content), ...) and nothing more.
func TestWriteFileString_WritesTheStringAsBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	WriteFileString(t, fs, "/a/b/c.txt", "hello there", 0o644)

	got, err := afero.ReadFile(fs, "/a/b/c.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello there" {
		t.Fatalf("content = %q, want %q", got, "hello there")
	}
}

// TestSeedTree_WritesEveryEntryUnderRoot proves SeedTree writes each map
// entry at root joined with the entry's (slash-separated) relative path, and
// that entries needing different nesting depths both land correctly.
func TestSeedTree_WritesEveryEntryUnderRoot(t *testing.T) {
	fs := afero.NewMemMapFs()
	SeedTree(t, fs, "/proj", map[string]string{
		"top.yaml":              "top",
		"nested/deep/leaf.yaml": "leaf",
	})

	for path, want := range map[string]string{
		"/proj/top.yaml":              "top",
		"/proj/nested/deep/leaf.yaml": "leaf",
	} {
		got, err := afero.ReadFile(fs, path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("content at %s = %q, want %q", path, got, want)
		}
	}
}

// mkdirFailFs wraps an afero.Fs, forcing every MkdirAll call to fail while
// delegating everything else — used to drive WriteFile's mkdir-error branch
// without a genuinely unwritable filesystem.
type mkdirFailFs struct {
	afero.Fs
	err error
}

func (f mkdirFailFs) MkdirAll(string, os.FileMode) error { return f.err }

// renameFailFs is mkdirFailFs's twin for the write-error branch: it forces
// Rename — safefs.WriteFile's final, publishing step — to fail.
type renameFailFs struct {
	afero.Fs
	err error
}

func (f renameFailFs) Rename(string, string) error { return f.err }
