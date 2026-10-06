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
// it undoes, so a records directory a write creates is owner-only, and so is
// each record. An existing directory's protection is the established home
// root it lies under (paths.EnsureHomeRoots), not re-applied per write.
func TestRecordDirIsOwnerOnly(t *testing.T) {
	s, fs, dir := osStore(t)
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
}
