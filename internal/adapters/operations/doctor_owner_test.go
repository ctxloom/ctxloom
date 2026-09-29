package operations

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

func probeReturning(st coord.OwnerStatus, err error) func(string) (coord.OwnerStatus, error) {
	return func(string) (coord.OwnerStatus, error) { return st, err }
}

func TestDoctorCheckProjectOwner(t *testing.T) {
	started := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	live := coord.OwnerStatus{Held: true, PID: 4242, Harp: "live-harp", Mode: coord.OwnerInteractive, Started: started, Reason: "the owner still has a controlling terminal"}
	orphan := live
	orphan.Orphan, orphan.Reason = true, "its terminal is gone"

	cases := []struct {
		name   string
		probe  func(string) (coord.OwnerStatus, error)
		status DoctorStatus
		want   []string
	}{
		{"unowned", probeReturning(coord.OwnerStatus{}, nil), DoctorOK, []string{"unowned", "claims the project"}},
		{"live owner", probeReturning(live, nil), DoctorInfo, []string{"live-harp", "pid 4242", "interactive", "orphan: no", "refused"}},
		{"orphan", probeReturning(orphan, nil), DoctorWarn, []string{"live-harp", "pid 4242", "orphan: yes", "ends it", "claims the project"}},
		{"unidentified owner", probeReturning(coord.OwnerStatus{Held: true, Reason: "stamp unreadable"}, nil), DoctorInfo, []string{"unidentified session", "orphan: no", "refused"}},
		{"probe failure", probeReturning(coord.OwnerStatus{}, errors.New("boom")), DoctorWarn, []string{"could not probe", "boom"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := doctorCheckProjectOwner(t.TempDir(), tc.probe)
			assert.Equal(t, doctorProjectOwnerMarker, c.Marker)
			assert.Equal(t, tc.status, c.Status)
			for _, w := range tc.want {
				assert.Contains(t, c.Detail, w)
			}
		})
	}
}

// With no project there is nothing to probe.
func TestDoctorCheckProjectOwner_NoProject(t *testing.T) {
	called := false
	c := doctorCheckProjectOwner("", func(string) (coord.OwnerStatus, error) { called = true; return coord.OwnerStatus{}, nil })
	require.False(t, called)
	assert.Equal(t, DoctorInfo, c.Status)
}
