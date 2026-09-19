package coord

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUsdToMicros_SaturatesInsteadOfWrapping pins the magnitude guard: a
// harness-reported cost large enough that usd*1e6 exceeds uint64's range must
// saturate at MaxUint64, never wrap. Go leaves an out-of-range float→uint64
// conversion implementation-defined, so the pre-guard code returned an
// arbitrary value (0x8000000000000000 on amd64) for an absurd cost — a
// coordinator-journaled usage number smaller than the truthful one.
func TestUsdToMicros_SaturatesInsteadOfWrapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		usd  float64
		want uint64
	}{
		{name: "zero", usd: 0, want: 0},
		{name: "negative", usd: -1, want: 0},
		{name: "nan", usd: math.NaN(), want: 0},
		{name: "inf", usd: math.Inf(1), want: 0},
		{name: "round half even down", usd: 0.0000005, want: 0},
		{name: "round half even up", usd: 0.0000015, want: 2},
		{name: "ordinary", usd: 1.234567, want: 1234567},
		{name: "largest representable", usd: 1e13, want: 1e19},
		{name: "beyond uint64", usd: 1e14, want: math.MaxUint64},
		{name: "absurd", usd: 1e300, want: math.MaxUint64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, usdToMicros(tc.usd))
		})
	}
}
