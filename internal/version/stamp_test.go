package version

import "testing"

// TestValidStamp_RejectsAnEmptyField is the case this whole file exists for: a
// build that could not determine its commit interpolated an empty ShortHash
// and produced a stamp that LOOKS structured. It must be rejected.
func TestValidStamp_RejectsAnEmptyField(t *testing.T) {
	for _, bad := range []string{
		"v0.7.0--20260826T043946",       // empty sha, the measured real-world case
		"v0.7.0--20260826T043946-dirty", // same, dirty
		"-27a90cd-20260826T043946",      // empty version
		"v0.7.0-27a90cd-",               // empty timestamp
		"v0.7.0-27a90cd",                // timestamp missing entirely
		"",                              // nothing at all
	} {
		if ValidStamp(bad) {
			t.Errorf("ValidStamp(%q) = true; a stamp with a missing field must be refused", bad)
		}
	}
}

// TestValidStamp_AcceptsRealStamps pins the other arm. Rejecting everything
// would satisfy the test above while breaking every build, so both directions
// are asserted -- these are stamps this project actually produced.
func TestValidStamp_AcceptsRealStamps(t *testing.T) {
	for _, good := range []string{
		"v0.7.0-27a90cd-20260826T125736",
		"v0.7.0-e033afe-20260826T032955-dirty",
		"v0.7.0-09f294f0-20260824T173556-dirty", // 8-char sha
		Dev,                                     // an unstamped build is legitimate, not a malformed stamp
	} {
		if !ValidStamp(good) {
			t.Errorf("ValidStamp(%q) = false; a real stamp was refused", good)
		}
	}
}
