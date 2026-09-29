package parity

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeEnum is a closed, text-marshalled vocabulary: its members persist by
// name, and a value outside it cannot be written at all.
type probeEnum int

var probeEnumNames = []string{"zero", "one", "two"}

func (e probeEnum) MarshalText() ([]byte, error) {
	if e < 0 || int(e) >= len(probeEnumNames) {
		return nil, errors.New("not a member")
	}
	return []byte(probeEnumNames[e]), nil
}

type probeCarrier struct {
	First, Second probeEnum
	Count         int
}

// TestFillSentinels_TextEnumGetsDistinctNamedMembers: an int-kinded field that
// persists by name cannot carry an arbitrary integer sentinel — no member has
// that value, so the byte-survival half could only pass by the field staying
// an ordinal. It gets a real member instead, never the zero (a converter that
// forgets the field writes the zero), distinct per field (so a swap is seen),
// and the sentinel that must survive is the member's NAME.
func TestFillSentinels_TextEnumGetsDistinctNamedMembers(t *testing.T) {
	v := reflect.New(reflect.TypeOf(probeCarrier{})).Elem()
	var bag sentinelBag
	fillSentinels(t, v, "probeCarrier", nil, &bag)
	got := v.Interface().(probeCarrier)

	assert.ElementsMatch(t, []probeEnum{1, 2}, []probeEnum{got.First, got.Second}, "each enum field is a distinct non-zero member")

	byPath := map[string]any{}
	for _, s := range bag.scalars {
		byPath[s.path] = s.value
	}
	require.Contains(t, byPath, "probeCarrier.First")
	assert.Equal(t, probeEnumNames[got.First], byPath["probeCarrier.First"], "the sentinel is the member's name")
	assert.Equal(t, probeEnumNames[got.Second], byPath["probeCarrier.Second"])
	assert.IsType(t, float64(0), byPath["probeCarrier.Count"], "a plain int keeps its numeric sentinel")
}
