package owneronly

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// EnsureDir creates a missing directory owner-only, and a file then created
// inside it is owner-only too — on Windows by inheriting the directory's
// ACE, which is the only way a file there gets it.
func TestEnsureDir_CreatesAnOwnerOnlyDirWhoseFilesAreOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	require.NoError(t, EnsureDir(dir))
	f := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(f, []byte("x"), FileMode))

	fileperm.OwnerOnly(t, dir)
	fileperm.OwnerOnly(t, f)
	require.NoError(t, Check(dir, f))
}

// A path that is not there is not "owner-only" and not "exposed": it is
// missing, and the caller is told so as fs.ErrNotExist.
func TestCheck_AMissingPathIsNotExist(t *testing.T) {
	err := Check(filepath.Join(t.TempDir(), "absent"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	var exposed *ExposedError
	assert.NotErrorAs(t, err, &exposed)
}

// The Windows owner-only verdict, platform-neutrally: the owner and the
// tolerated machine principals (SYSTEM, Administrators) are not exposure;
// anyone else is, named once each.
func TestExposure(t *testing.T) {
	const owner, system, admins, everyone, users = "S-1-5-21-1", "S-1-5-18", "S-1-5-32-544", "S-1-1-0", "S-1-5-32-545"
	tolerated := []string{system, admins}
	for _, tc := range []struct {
		name     string
		grantees []string
		want     string
	}{
		{"owner only", []string{owner}, ""},
		{"owner with SYSTEM and Administrators is still owner-only", []string{owner, system, admins}, ""},
		{"no grantee at all", nil, ""},
		{"Everyone is exposure", []string{owner, everyone}, "grants access to " + everyone},
		{"each outsider named once, in ACL order", []string{users, owner, everyone, users}, "grants access to " + users + ", " + everyone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, exposure(owner, tolerated, tc.grantees))
		})
	}
}
