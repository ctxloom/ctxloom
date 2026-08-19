package operations

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// A profile whose content the review gate withholds ENTIRELY
// assembled to a stub and exited 0. The per-item withheld advisory names items
// but never the PROFILE, so `run -p coordinator` on a virgin machine emitted a
// generic pending-review line and produced a context with the coordinator's
// whole role missing — the agent still answers, just lobotomized. Silent
// context degradation is the worst case for a trust gate.
func TestGuttedProfiles(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared map[string][]string
		loaded   []string
		want     []string
	}{
		{
			name:     "a profile with every fragment withheld is gutted",
			declared: map[string][]string{"coordinator": {"role", "tools"}},
			loaded:   nil,
			want:     []string{"coordinator"},
		},
		{
			name:     "a partially loaded profile is not gutted",
			declared: map[string][]string{"coordinator": {"role", "tools"}},
			loaded:   []string{"tools"},
			want:     nil,
		},
		{
			name:     "a fully loaded profile is not gutted",
			declared: map[string][]string{"coordinator": {"role"}},
			loaded:   []string{"role"},
			want:     nil,
		},
		{
			name:     "a profile that declares nothing is not gutted",
			declared: map[string][]string{"empty": nil},
			loaded:   nil,
			want:     nil,
		},
		{
			name:     "only the gutted profile of several is named, in order",
			declared: map[string][]string{"coordinator": {"role"}, "base": {"style"}},
			loaded:   []string{"style"},
			want:     []string{"coordinator"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, guttedProfiles(tc.declared, tc.loaded))
		})
	}
}

// TestWarnGuttedProfiles_NamesProfileAndWithheldItems pins the advisory: it
// fires only when the gate actually withheld something (a profile that is
// empty for any other reason is not the trust gate's story to tell), and it
// names the profile — the thing the old generic tally never did.
func TestWarnGuttedProfiles_NamesProfileAndWithheldItems(t *testing.T) {
	gate := &contentGate{}
	gate.record(
		bundles.Exposure{
			Ref:    trust.Ref{RepoURL: "https://github.com/acme/repo", Bundle: "ensemble", Kind: trust.KindFragment, Name: "role"},
			RefStr: "ctxloom+git://github.com/acme/repo//bundles/ensemble#fragments/role",
		},
		bundles.Verdict{Reason: bundles.ReasonPending})

	var out bytes.Buffer
	warnGuttedProfilesTo(&out, map[string][]string{"coordinator": {"role"}}, nil, gate)
	text := out.String()
	assert.Contains(t, text, "coordinator", "the gutted PROFILE must be named")
	assert.Contains(t, text, "bundles/ensemble", "the withheld item's bundle must be named")
	assert.Contains(t, text, "ctxloom review")

	// Nothing withheld → the gate has no story; stay silent.
	var quiet bytes.Buffer
	warnGuttedProfilesTo(&quiet, map[string][]string{"coordinator": {"role"}}, nil, &contentGate{})
	assert.Empty(t, quiet.String())
}
