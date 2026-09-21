package operations

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// doctorCheckKeychainOrphans names the macOS Keychain credential items no
// session directory explains — items a crashed run never deleted and the
// reaper has not reached — with the command that removes each; and says
// nothing alarming where there are none.
func TestDoctorCheckKeychainOrphans_NamesEachOrphanWithItsDeleteCommand(t *testing.T) {
	c := doctorCheckKeychainOrphans(func() ([]string, error) {
		return []string{"Claude Code-credentials-0a1b2c3d", "Claude Code-credentials-ffffffff"}, nil
	}, "alice")
	assert.Equal(t, doctorKeychainOrphansMarker, c.Marker)
	assert.Equal(t, DoctorWarn, c.Status)
	assert.Contains(t, c.Detail, "2 Keychain credential item(s)")
	assert.Contains(t, c.Detail, `security delete-generic-password -a alice -s "Claude Code-credentials-0a1b2c3d"`)
	assert.Contains(t, c.Detail, `security delete-generic-password -a alice -s "Claude Code-credentials-ffffffff"`)
	assert.Contains(t, c.Detail, "ctxloom session reap")
}

func TestDoctorCheckKeychainOrphans_NoneIsOK_UnlistableIsWarn(t *testing.T) {
	c := doctorCheckKeychainOrphans(func() ([]string, error) { return nil, nil }, "alice")
	assert.Equal(t, DoctorOK, c.Status)
	assert.NotEmpty(t, c.Detail)

	c = doctorCheckKeychainOrphans(func() ([]string, error) { return nil, errors.New("security: keychain locked") }, "alice")
	assert.Equal(t, DoctorWarn, c.Status)
	assert.Contains(t, c.Detail, "keychain locked")
}
