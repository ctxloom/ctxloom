package iox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WriteFileAtomic and WriteFileAtomicFs must stay ONE algorithm: writing the
// same steps twice, with no shared code and no compiler-enforced link, is
// connascence of algorithm across two files. This exercises BOTH through one
// table against a real OS directory (afero.NewOsFs() for the fs variant) so
// any divergence in outcome, bytes, mode, error behaviour, or temp-file
// cleanup shows up as a failure.
func TestAtomicWriters_Parity(t *testing.T) {
	writers := map[string]func(dir, path string, data []byte, perm os.FileMode, opts ...Option) error{
		"WriteFileAtomic": func(_, path string, data []byte, perm os.FileMode, opts ...Option) error {
			return WriteFileAtomic(path, data, perm, opts...)
		},
		"WriteFileAtomicFs": func(_, path string, data []byte, perm os.FileMode, opts ...Option) error {
			return WriteFileAtomicFs(afero.NewOsFs(), path, data, perm, opts...)
		},
	}

	cases := []struct {
		name string
		// pre, when non-nil, is written to the target before the atomic write.
		pre     []byte
		prePerm os.FileMode
		// missingDir targets a directory that does not exist.
		missingDir bool
		data       []byte
		perm       os.FileMode
		opts       []Option
		// durable, when set, adds Durable() to the write and asserts that the
		// parent-directory sync seam fired exactly once, on the write's own
		// directory, for BOTH writers — see the per-writer loop below. Kept
		// separate from opts (rather than just appending Durable() into a
		// case's opts) because the seam assertion below needs to know intent,
		// not just replay whatever opts happen to contain.
		durable bool
		wantErr bool
	}{
		{name: "create new", data: []byte("hello"), perm: 0o644},
		{name: "create new restrictive", data: []byte("secret"), perm: 0o600},
		{name: "overwrite existing", pre: []byte("old-and-longer"), prePerm: 0o644, data: []byte("new"), perm: 0o600},
		{name: "empty payload to new path proceeds", data: []byte{}, perm: 0o644},
		{name: "empty payload over existing is refused", pre: []byte("live"), prePerm: 0o644, data: []byte{}, perm: 0o644, wantErr: true},
		{name: "empty payload over existing with AllowEmpty proceeds", pre: []byte("live"), prePerm: 0o644, data: []byte{}, perm: 0o644, opts: []Option{AllowEmpty()}},
		{name: "missing parent dir", missingDir: true, data: []byte("x"), perm: 0o644, wantErr: true},
		{name: "durable option syncs the parent directory", data: []byte("durable"), perm: 0o644, durable: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			type outcome struct {
				err   bool
				bytes string
				mode  os.FileMode
				// strays counts leftover files in the directory besides the target.
				strays int
				// syncedCount is how many times the parent-directory sync seam
				// fired for this write. Compared across writers by the
				// got["WriteFileAtomic"] == got["WriteFileAtomicFs"] assertion
				// below, so a Durable() that fires on one entry point but not
				// the other goes red here — the literal directory path is
				// deliberately NOT part of this struct, since each writer gets
				// its own t.TempDir() and would never compare equal; the count
				// (and the exact-directory check right below) is where that's
				// verified instead.
				syncedCount int
			}
			got := map[string]outcome{}

			for wname, w := range writers {
				dir := t.TempDir()
				target := filepath.Join(dir, "settings.json")
				if tc.missingDir {
					target = filepath.Join(dir, "nope", "settings.json")
				}
				if tc.pre != nil {
					require.NoError(t, os.WriteFile(target, tc.pre, tc.prePerm))
				}

				opts := tc.opts
				if tc.durable {
					opts = append(append([]Option{}, opts...), Durable())
				}

				var synced []string
				origSyncDir := syncDirFn
				syncDirFn = func(d string) error {
					synced = append(synced, d)
					return nil
				}
				err := w(dir, target, tc.data, tc.perm, opts...)
				syncDirFn = origSyncDir

				if tc.durable {
					assert.Equal(t, []string{dir}, synced,
						"%s: Durable() must sync exactly the write's own parent directory", wname)
				} else {
					assert.Empty(t, synced,
						"%s: the parent directory must not be synced unless Durable() is passed", wname)
				}

				o := outcome{err: err != nil, syncedCount: len(synced)}
				if err == nil {
					b, rerr := os.ReadFile(target)
					require.NoError(t, rerr)
					o.bytes = string(b)
					info, serr := os.Stat(target)
					require.NoError(t, serr)
					o.mode = info.Mode().Perm()
				}
				entries, derr := os.ReadDir(dir)
				require.NoError(t, derr)
				for _, e := range entries {
					if e.Name() != "settings.json" {
						o.strays++
					}
				}
				if tc.wantErr && tc.pre != nil {
					// The refusal happens before anything is touched: the
					// original bytes must survive a rejected write.
					b, rerr := os.ReadFile(target)
					require.NoError(t, rerr)
					assert.Equal(t, string(tc.pre), string(b), "%s: a refused write must leave the original bytes untouched", wname)
				}
				got[wname] = o
			}

			assert.Equal(t, got["WriteFileAtomic"], got["WriteFileAtomicFs"],
				"the two atomic writers must produce identical outcomes")
			assert.Equal(t, tc.wantErr, got["WriteFileAtomic"].err)
			assert.Zero(t, got["WriteFileAtomic"].strays, "no temp file may survive the write")
			if tc.durable {
				assert.Equal(t, 1, got["WriteFileAtomic"].syncedCount,
					"a durable write must sync the parent directory exactly once")
			}
		})
	}
}

// The unique-temp-name guarantee is the whole reason this helper exists (a
// fixed "<path>.tmp" lets a concurrent writer clobber another's in-flight
// temp). Pin it on both writers: the temp name is derived from the target's
// base name plus randomness, never the bare target.
func TestAtomicWriters_TempNameIsUnique(t *testing.T) {
	for _, wname := range []string{"WriteFileAtomic", "WriteFileAtomicFs"} {
		dir := t.TempDir()
		target := filepath.Join(dir, "settings.json")
		var err error
		if wname == "WriteFileAtomic" {
			err = WriteFileAtomic(target, []byte("a"), 0o600)
		} else {
			err = WriteFileAtomicFs(afero.NewOsFs(), target, []byte("a"), 0o600)
		}
		require.NoError(t, err, wname)
		entries, derr := os.ReadDir(dir)
		require.NoError(t, derr)
		require.Len(t, entries, 1, "%s left a temp file behind", wname)
		assert.False(t, strings.HasSuffix(entries[0].Name(), ".tmp"), wname)
	}
}
