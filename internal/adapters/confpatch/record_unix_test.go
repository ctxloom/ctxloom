//go:build !windows

package confpatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRecordDirIsOwnerOnly: an undo record keeps the previous value of the key
// it undoes, so the directory holding records is owner-only. A directory that
// already exists looser — created by an older binary, or by hand — is tightened
// on the next write rather than trusted, because MkdirAll leaves an existing
// directory's mode alone.
func TestRecordDirIsOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{name: "fresh directory", existing: false},
		{name: "pre-existing 0755 directory", existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fs, dir := osStore(t)
			if tc.existing {
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.Chmod(dir, 0o755))
			}
			target := filepath.Join(t.TempDir(), ".mcp.json")
			testsupport.WriteFileString(t, fs, target, foreign, 0o644)

			res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
			require.NoError(t, err)

			di, err := os.Stat(dir)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o700), di.Mode().Perm(), "the records directory must be owner-only")
			fi, err := os.Stat(res.RecordPath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "a record file must be owner-only")
		})
	}
}
