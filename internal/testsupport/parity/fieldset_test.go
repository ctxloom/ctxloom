package parity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The negative self-probe: before any real pair is registered, the
// comparator must be seen reporting a difference it was GIVEN — otherwise a
// table of pairs that all pass proves nothing about the mechanism.
func TestFieldSetDiff_DifferingSets_ReportsExactlyTheDifference(t *testing.T) {
	onlyA, onlyB := FieldSetDiff(
		[]string{"Identity", "Endpoint", "Resume", "Source"},
		[]string{"Endpoint", "Resume", "Source", "Carrier"},
	)
	assert.Equal(t, []string{"Identity"}, onlyA, "a name only the first set carries")
	assert.Equal(t, []string{"Carrier"}, onlyB, "a name only the second set carries")
}

func TestFieldSetDiff_EqualSets_ReportsNothing(t *testing.T) {
	onlyA, onlyB := FieldSetDiff([]string{"B", "A"}, []string{"A", "B"})
	assert.Empty(t, onlyA)
	assert.Empty(t, onlyB)
}

func TestFieldSetDiff_ReportsSortedAndDeduplicated(t *testing.T) {
	onlyA, onlyB := FieldSetDiff([]string{"Z", "Y", "Z"}, []string{"M", "M"})
	assert.Equal(t, []string{"Y", "Z"}, onlyA)
	assert.Equal(t, []string{"M"}, onlyB)
}

func TestFieldSetReport_OneSidedDifferenceIsReported(t *testing.T) {
	left := func() []string { return []string{"A", "B"} }
	for _, tc := range []struct {
		name  string
		right func() []string
		wants string
	}{
		{"only the left carries a name", func() []string { return []string{"A"} }, "only L: B"},
		{"only the right carries a name", func() []string { return []string{"A", "B", "C"} }, "only R: C"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := FieldSetReport(FieldSetPair{Name: tc.name, Left: left, Right: tc.right, LeftName: "L", RightName: "R"})
			assert.False(t, ok, "a one-sided difference must not read as parity")
			assert.Contains(t, msg, tc.wants)
		})
	}
	msg, ok := FieldSetReport(FieldSetPair{Name: "equal", Left: left, Right: left, LeftName: "L", RightName: "R"})
	assert.True(t, ok)
	assert.Empty(t, msg)
}
