package operations

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTTYInjectionCheck: the approval modal's focus locks stop a key typed
// for the engine from deciding an approval, but not a process that can type
// into the terminal itself (TIOCSTI) — so doctor says whether one can.
func TestTTYInjectionCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		err     error
		status  DoctorStatus
		detail  string
		remedy  bool
	}{
		{"injection off", "0\n", nil, DoctorOK, "dev.tty.legacy_tiocsti=0", false},
		{"injection on", "1\n", nil, DoctorWarn, "dev.tty.legacy_tiocsti=1", true},
		{"a kernel without the setting always allows it", "", fs.ErrNotExist, DoctorWarn, "predates 6.2", true},
		{"unreadable", "", errors.New("permission denied"), DoctorWarn, "permission denied", false},
		{"an unknown value is not taken as off", "2\n", nil, DoctorWarn, `"2"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ttyInjectionCheck([]byte(tc.content), tc.err)
			assert.Equal(t, doctorTTYInjectionMarker, c.Marker)
			assert.Equal(t, tc.status, c.Status)
			assert.Contains(t, c.Detail, tc.detail)
			assert.Equal(t, tc.remedy, c.Remedy != "", "remedy: %q", c.Remedy)
		})
	}
}

// TestDoctorCheckTTYInjection_ReportsOnThisHost: whatever this host says, the
// row is the check's and in the vocabulary.
func TestDoctorCheckTTYInjection_ReportsOnThisHost(t *testing.T) {
	c := doctorCheckTTYInjection()
	assert.Equal(t, doctorTTYInjectionMarker, c.Marker)
	assert.Contains(t, []DoctorStatus{DoctorOK, DoctorWarn, DoctorInfo}, c.Status)
	assert.NotEmpty(t, c.Detail)
}
