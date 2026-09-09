package version

import (
	"regexp"
	"time"
)

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

// stampBuildTime captures the UTC build timestamp out of a stamp. It is a
// SECOND expression rather than capture groups bolted onto stampShape because
// stampShape is the shape AUTHORITY, read by two gates; widening it to serve a
// parser would make every future change to the parser a change to what those
// gates accept.
var stampBuildTime = regexp.MustCompile(`-([0-9]{8}T[0-9]{6})(?:-dirty)?$`)

// BuildTime returns the UTC build time encoded in stamp s, and whether s
// carried a parseable one.
//
// The ok result is not a formality. A caller comparing two builds may only
// state which is older when BOTH parse — a stamp that does not is a build
// whose age is unknown, and asserting an order over it is exactly the
// confidently-wrong claim that makes a refusal message worse than none.
func BuildTime(s string) (time.Time, bool) {
	m := stampBuildTime.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102T150405", m[1], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
