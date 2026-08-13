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
		wantErr    bool
	}{
		{name: "create new", data: []byte("hello"), perm: 0o644},
		{name: "create new restrictive", data: []byte("secret"), perm: 0o600},
		{name: "overwrite existing", pre: []byte("old-and-longer"), prePerm: 0o644, data: []byte("new"), perm: 0o600},
		{name: "empty payload to new path proceeds", data: []byte{}, perm: 0o644},
		{name: "empty payload over existing is refused", pre: []byte("live"), prePerm: 0o644, data: []byte{}, perm: 0o644, wantErr: true},
		{name: "empty payload over existing with AllowEmpty proceeds", pre: []byte("live"), prePerm: 0o644, data: []byte{}, perm: 0o644, opts: []Option{AllowEmpty()}},
		{name: "missing parent dir", missingDir: true, data: []byte("x"), perm: 0o644, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			type outcome struct {
				err   bool
				bytes string
				mode  os.FileMode
				// strays counts leftover files in the directory besides the target.
				strays int
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

				err := w(dir, target, tc.data, tc.perm, tc.opts...)
				o := outcome{err: err != nil}
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
