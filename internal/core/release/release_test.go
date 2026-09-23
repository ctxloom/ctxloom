package release

import (
	"errors"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func v(t *testing.T, s string) *semver.Version {
	t.Helper()
	out, err := semver.StrictNewVersion(s)
	require.NoError(t, err)
	return out
}

func TestRetracted_NamesTheExactVersion(t *testing.T) {
	r := Release{Name: "b", Version: v(t, "1.3.0"), Retracts: []Retraction{
		{Version: v(t, "1.1.0"), Reason: "leaked token"},
		{Version: v(t, "1.2.0"), Reason: "broken hook"},
	}}
	got, why := r.Retracted(v(t, "1.2.0"))
	assert.True(t, got)
	assert.Equal(t, "broken hook", why)

	got, why = r.Retracted(v(t, "1.2.1"))
	assert.False(t, got, "a retraction names one exact version, not a range")
	assert.Empty(t, why)
}

func TestRetracted_WithdrawnRetractsEveryVersion(t *testing.T) {
	r := Release{Name: "b", Version: v(t, "2.0.0"), Withdrawn: "abandoned"}
	for _, s := range []string{"0.1.0", "2.0.0", "9.9.9"} {
		got, why := r.Retracted(v(t, s))
		assert.True(t, got, s)
		assert.Equal(t, "abandoned", why)
	}
	got, why := r.Retracted(nil)
	assert.True(t, got, "withdrawal covers even an unversioned pin")
	assert.Equal(t, "abandoned", why)
}

func TestRetracted_NilVersionIsNeverNamed(t *testing.T) {
	r := Release{Name: "b", Version: v(t, "1.0.0"), Retracts: []Retraction{{Version: v(t, "1.0.0"), Reason: "x"}}}
	got, _ := r.Retracted(nil)
	assert.False(t, got)
}

func TestCheckAdvance(t *testing.T) {
	cases := []struct {
		name    string
		floor   string
		next    string
		allow   bool
		wantErr error
	}{
		{name: "no floor, first signed pin", floor: "", next: "1.0.0"},
		{name: "no floor, unattested", floor: "", next: ""},
		{name: "forward", floor: "1.0.0", next: "1.1.0"},
		{name: "equal is accepted", floor: "1.2.0", next: "1.2.0"},
		{name: "prerelease below release is a rollback", floor: "1.2.0", next: "1.2.0-rc.1", wantErr: ErrRollback},
		{name: "rollback", floor: "1.2.0", next: "1.1.9", wantErr: ErrRollback},
		{name: "rollback allowed", floor: "1.2.0", next: "1.1.9", allow: true},
		{name: "signed to unattested", floor: "1.2.0", next: "", wantErr: ErrSignatureDowngrade},
		{name: "signed to unattested allowed", floor: "1.2.0", next: "", allow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var floor, next *semver.Version
			if tc.floor != "" {
				floor = v(t, tc.floor)
			}
			if tc.next != "" {
				next = v(t, tc.next)
			}
			err := CheckAdvance(floor, next, tc.allow)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.wantErr), "got %v", err)
			if tc.next != "" {
				assert.Contains(t, err.Error(), tc.floor)
				assert.Contains(t, err.Error(), tc.next)
			}
		})
	}
}
