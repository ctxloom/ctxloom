package version

import "regexp"

// stampShape is the documented stamp:
//
//	v<major>.<minor>.<patch>-<short-sha>-<YYYYMMDDTHHMMSS>[-dirty]
//
// Every field is REQUIRED and non-empty, which is the entire point. A build
// that cannot determine its commit used to interpolate an empty ShortHash and
// emit "v0.7.0--20260826T043946" -- exit 0, binary produced, and no way to tell
// which commit answered.
//
// That is not cosmetic. This project's verification rule is to run checks
// against a binary built from the tree under test, because a stale binary plus
// an exit-code check agrees with anything; the stamp is the ONLY mechanism for
// telling which binary answered. In a linked git worktree -- exactly where
// agents are told to work -- that mechanism silently returned nothing.
var stampShape = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-[0-9a-f]+-[0-9]{8}T[0-9]{6}(-dirty)?$`)

// ValidStamp reports whether s is a usable version stamp: a COMPLETE stamp,
// every field present. Nothing else qualifies -- there is no unstamped
// sentinel, because a binary that cannot name its own build is refused rather
// than tolerated (see Version).
//
// It is the one authority on the shape, read by both gates that enforce it:
// cmd/validate refuses to bake a malformed stamp into a binary, and internal/cli's
// root gate refuses to RUN a binary that did not get one. The shape lives here,
// in the package that owns Version, so those two assert against one authority
// rather than two regexes that will eventually disagree.
func ValidStamp(s string) bool {
	return stampShape.MatchString(s)
}
