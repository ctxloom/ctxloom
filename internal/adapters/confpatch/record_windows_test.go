//go:build windows

package confpatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// On Windows owner-only is a DACL, not a mode, and it is the established
// root's: a records directory — fresh, or one that already existed carrying
// the ACL it inherited from its parent — established as paths.EnsureHomeRoots
// does it grants only the current user, and the record written into it
// inherits that.
func TestRecordDirIsOwnerOnly_ADACL(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh directory", true: "pre-existing inherited-ACL directory"}[existing], func(t *testing.T) {
			s, fs, dir := osStore(t)
			if existing {
				require.NoError(t, os.MkdirAll(dir, 0o755))
			}
			require.NoError(t, safefs.New().Private.Ensure(dir))
			target := filepath.Join(t.TempDir(), ".mcp.json")
			testsupport.WriteFileString(t, fs, target, foreign, 0o644)

			res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
			require.NoError(t, err)

			fileperm.OwnerOnly(t, dir)
			fileperm.OwnerOnly(t, res.RecordPath)
		})
	}
}
