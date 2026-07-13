package triggers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOutcomeValid(t *testing.T) {
	cases := []struct {
		name string
		o    Outcome
		want bool
	}{
		{"fired", Fired, true},
		{"not-fired", NotFired, true},
		{"needs-investigation", NeedsInvestigation, true},
		{"cannot-determine", CannotDetermine, true},
		{"empty", Outcome(""), false},
		{"unknown", Outcome("maybe"), false},
		{"wrong-case", Outcome("Fired"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.o.Valid())
		})
	}
}

// The four outcome literals are a load-bearing contract with the prompt and
// the parser; pin their exact wire values so a rename anywhere doesn't
// silently drift the three copies apart.
func TestOutcomeWireValues(t *testing.T) {
	assert.Equal(t, "fired", string(Fired))
	assert.Equal(t, "not-fired", string(NotFired))
	assert.Equal(t, "needs-investigation", string(NeedsInvestigation))
	assert.Equal(t, "cannot-determine", string(CannotDetermine))
}

func TestOutcomesListsAllFour(t *testing.T) {
	got := Outcomes()
	assert.ElementsMatch(t, []Outcome{Fired, NotFired, NeedsInvestigation, CannotDetermine}, got)
}

func TestClampConfidence(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{-1, 0},
		{0, 0},
		{0.5, 0.5},
		{1, 1},
		{1.5, 1},
		{100, 1},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, clampConfidence(tc.in))
	}
}
