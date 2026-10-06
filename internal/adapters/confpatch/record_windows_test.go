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
// root's (paths.EnsureHomeRoots establishes it with Private.Ensure). A fresh
// records directory established that way grants only the current user, and
// the record written into it inherits exactly that. One that already existed
// carrying the ACL it inherited from its parent is left as it is when that
// ACL grants no one beyond the owner, SYSTEM and Administrators (the ruled
// tolerance), so the dir and its record pass Private.Check rather than
// granting the owner alone.
func TestRecordDirIsOwnerOnly_ADACL(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh directory", true: "pre-existing inherited-ACL directory"}[existing], func(t *testing.T) {
			s, fs, dir := osStore(t)
			if existing {
				require.NoError(t, os.MkdirAll(dir, 0o755))
			}
			private := safefs.New().Private
			require.NoError(t, private.Ensure(dir))
			target := filepath.Join(t.TempDir(), ".mcp.json")
			testsupport.WriteFileString(t, fs, target, foreign, 0o644)

			res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
			require.NoError(t, err)

			require.NoError(t, private.Check(dir, res.RecordPath))
			if !existing {
				fileperm.OwnerOnly(t, dir)
				fileperm.OwnerOnly(t, res.RecordPath)
			}
		})
	}
}
