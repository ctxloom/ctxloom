package iox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAtomicWriters_ReplaceByRename_NotInPlace pins the ATOMICITY half of the
// contract, which is the half these writers actually provide: the destination
// is replaced by a rename, never rewritten in place. A reader that already
// holds the file open therefore keeps reading the complete PREVIOUS content
// and can never observe a mix of old and new bytes.
//
// Durability of the rename is a different property and is deliberately NOT
// claimed: neither writer fsyncs the parent directory, so after a power loss
// the directory entry may still name the old file (the doc used to
// say "the new content survives a crash", which the code does not deliver).
// This test guards the guarantee that remains, and goes red the moment the
// rename is replaced by an in-place write.
func TestAtomicWriters_ReplaceByRename_NotInPlace(t *testing.T) {
	writers := map[string]func(path string, data []byte, perm os.FileMode) error{
		"WriteFileAtomic": func(path string, data []byte, perm os.FileMode) error {
			return WriteFileAtomic(path, data, perm)
		},
		"WriteFileAtomicFs": func(path string, data []byte, perm os.FileMode) error {
			return WriteFileAtomicFs(afero.NewOsFs(), path, data, perm)
		},
	}

	old := []byte("the complete previous content")
	fresh := []byte("new")

	for name, w := range writers {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "settings.json")
			require.NoError(t, os.WriteFile(target, old, 0o644))

			beforeInfo, err := os.Stat(target)
			require.NoError(t, err)

			// A concurrent reader that opened the file before the write. Skipped
			// on Windows, where replacing a file another handle holds open is
			// not the same operation.
			var reader *os.File
			if runtime.GOOS != "windows" {
				reader, err = os.Open(target)
				require.NoError(t, err)
				defer func() { _ = reader.Close() }()
			}

			require.NoError(t, w(target, fresh, 0o644))

			if reader != nil {
				buf := make([]byte, len(old))
				n, rerr := reader.ReadAt(buf, 0)
				require.NoError(t, rerr, "the pre-existing descriptor must still be readable")
				assert.Equal(t, string(old), string(buf[:n]),
					"a reader holding the file open must still see the complete previous content")
			}

			afterInfo, err := os.Stat(target)
			require.NoError(t, err)
			assert.False(t, os.SameFile(beforeInfo, afterInfo),
				"the destination must be a NEW file put in place by rename, not the same file rewritten")

			got, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, string(fresh), string(got))
		})
	}
}
