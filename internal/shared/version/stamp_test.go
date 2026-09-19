package version

import "testing"

// TestValidStamp_RejectsAnythingLessThanAWholeStamp is the case this whole file
// exists for: a build that could not determine its commit interpolated an empty
// ShortHash and produced a stamp that LOOKS structured. It must be rejected --
// and so must an UNSTAMPED build, which is the whole point of the rule. There
// is no sentinel meaning "legitimately unstamped": a binary that cannot name
// its own build is refused, so "dev" and "" are exactly as unusable as a stamp
// with a hole in it.
func TestValidStamp_RejectsAnythingLessThanAWholeStamp(t *testing.T) {
	for _, bad := range []string{
		"v0.7.0--20260826T043946",       // empty sha, the measured real-world case
		"v0.7.0--20260826T043946-dirty", // same, dirty
		"-27a90cd-20260826T043946",      // empty version
		"v0.7.0-27a90cd-",               // empty timestamp
		"v0.7.0-27a90cd",                // timestamp missing entirely
		"",                              // an unstamped build: no ldflags reached it
		"dev",                           // the retired sentinel: no longer a pass
	} {
		if ValidStamp(bad) {
			t.Errorf("ValidStamp(%q) = true; only a complete stamp may be accepted", bad)
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
	} {
		if !ValidStamp(good) {
			t.Errorf("ValidStamp(%q) = false; a real stamp was refused", good)
		}
	}
}

// TestVersionDefaultsToUnstamped pins the removal of the "dev" default itself,
// against the real package variable. This package has no TestMain, so nothing
// stamps its own test binary: Version here IS the compiled-in default, and
// every downstream refusal is reachable only because that default fails
// ValidStamp. Restoring any default that passes would silently disarm the lot,
// with nothing else in the tree going red.
func TestVersionDefaultsToUnstamped(t *testing.T) {
	if ValidStamp(Version) {
		t.Fatalf("Version defaults to %q, which ValidStamp accepts; an unstamped build must not be able to pass itself off as identified", Version)
	}
}
