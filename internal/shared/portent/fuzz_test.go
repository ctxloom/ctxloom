package portent

import "testing"

func FuzzCandidates(f *testing.F) {
	f.Add([]byte(""), uint16(1), uint16(1023))
	f.Add([]byte("a"), uint16(1000), uint16(1009))
	f.Add([]byte("portpick"), uint16(65535), uint16(65535))
	f.Fuzz(func(t *testing.T, in []byte, lo, hi uint16) {
		r := Range{lo, hi}
		if r.Validate() != nil {
			return
		}
		checkFullCoverage(t, in, r)
	})
}
