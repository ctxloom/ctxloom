package operations

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

func rootsReturning(roots []coord.RootStatus, err error) func(string, string) ([]coord.RootStatus, error) {
	return func(string, string) ([]coord.RootStatus, error) { return roots, err }
}

// TestDoctorCheckProjectOwner enumerates the project's coordinator roots: a
// live one is information (a new run founds its own tree beside it, never
// refused), and one nobody can resume by running — an orphaned owner, or a
// root its owner left with runs not ended — is a warning naming the resume
// that adopts it. Doctor removes nothing and says so.
func TestDoctorCheckProjectOwner(t *testing.T) {
	started := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	live := coord.RootStatus{Dir: "/c/p/live-harp", RootHarp: "live-harp", Owner: coord.OwnerStatus{
		Held: true, PID: 4242, Harp: "live-harp", Mode: coord.OwnerInteractive, Started: started, Reason: "the owner still has a controlling terminal"}}
	adopted := live
	adopted.Owner.Harp = "resumer-harp"
	orphan := live
	orphan.RootHarp, orphan.Owner.Harp = "orphan-harp", "orphan-harp"
	orphan.Owner.Orphan, orphan.Owner.Reason = true, "its terminal is gone"
	left := coord.RootStatus{Dir: "/c/p/left-harp", RootHarp: "left-harp"}
	unidentified := coord.RootStatus{Dir: "/c/p/anon-harp", RootHarp: "anon-harp", Owner: coord.OwnerStatus{Held: true, Reason: "stamp unreadable"}}

	cases := []struct {
		name    string
		list    func(string, string) ([]coord.RootStatus, error)
		status  DoctorStatus
		want    []string
		notWant []string
	}{
		{"no roots", rootsReturning(nil, nil), DoctorOK, []string{"no coordinator roots", "founds its own"}, nil},
		{"one live root", rootsReturning([]coord.RootStatus{live}, nil), DoctorInfo,
			[]string{"1 coordinator root", "live-harp", "live", "pid 4242", "interactive", "founds its own root alongside"}, []string{"refused", "orphan"}},
		{"a root adopted by a resumed session names its owner", rootsReturning([]coord.RootStatus{adopted}, nil), DoctorInfo,
			[]string{"live-harp", "resumer-harp"}, nil},
		{"an orphaned root", rootsReturning([]coord.RootStatus{live, orphan}, nil), DoctorWarn,
			[]string{"2 coordinator roots", "orphan-harp", "orphaned", "its terminal is gone", "ctxloom run --session orphan-harp", "doctor removes no root"}, []string{"refused"}},
		{"a root nobody owns", rootsReturning([]coord.RootStatus{left}, nil), DoctorWarn,
			[]string{"left-harp", "unowned", "ctxloom run --session left-harp", "ctxloom session sweep", "doctor removes no root"}, nil},
		{"an unidentified owner", rootsReturning([]coord.RootStatus{unidentified}, nil), DoctorInfo, []string{"anon-harp", "an unidentified session"}, nil},
		{"list failure keeps what was listed", rootsReturning([]coord.RootStatus{live}, errors.New("boom")), DoctorWarn,
			[]string{"live-harp", "could not probe", "boom"}, nil},
		{"list failure with nothing listed", rootsReturning(nil, errors.New("boom")), DoctorWarn, []string{"could not list", "boom"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := doctorCheckProjectOwner(t.TempDir(), tc.list)
			assert.Equal(t, doctorProjectOwnerMarker, c.Marker)
			assert.Equal(t, tc.status, c.Status, c.Detail)
			for _, w := range tc.want {
				assert.Contains(t, c.Detail, w)
			}
			for _, w := range tc.notWant {
				assert.NotContains(t, c.Detail, w)
			}
		})
	}
}

// With no project there is nothing to list.
func TestDoctorCheckProjectOwner_NoProject(t *testing.T) {
	called := false
	c := doctorCheckProjectOwner("", func(string, string) ([]coord.RootStatus, error) { called = true; return nil, nil })
	require.False(t, called)
	assert.Equal(t, DoctorInfo, c.Status)
}
