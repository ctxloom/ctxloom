package engine

import "fmt"

// Distribution is an engine's shipping POLICY: whether it is offered to a
// user at all, and whether it is baked into the default composed agent image.
// It is one enum rather than a pair of booleans (test-only? composable?)
// because two booleans can spell a state that means nothing — a test double
// that default-ships — and because CAPABILITY is declared elsewhere: whether
// an engine CAN be composed is its container story's installer (nil = no
// installer), and the default image set is the conjunction of that
// capability with this policy, each read from where it is declared.
//
// The zero value is UNSET, and Base.Validate refuses it. That is the
// invariant, not a style choice: if shipping were the zero value, an engine
// that declared nothing would silently default-ship — the forgotten-entry
// failure the constructor exists to close.
type Distribution int

const (
	// DistributionUnset is nobody's decision; Base.Validate refuses it.
	DistributionUnset Distribution = iota
	// DistributionDefault ships, is offered to users, and is in the default
	// composed image set (given an installer).
	DistributionDefault
	// DistributionOptIn ships and is offered, but must be ASKED for: never in
	// the default composed image set.
	DistributionOptIn
	// DistributionTestOnly is a test/development double: registered and
	// reachable at runtime, hidden from every user-facing enumeration, never
	// composed.
	DistributionTestOnly
)

// String renders the policy for diagnostics.
func (d Distribution) String() string {
	switch d {
	case DistributionUnset:
		return "unset"
	case DistributionDefault:
		return "default"
	case DistributionOptIn:
		return "opt-in"
	case DistributionTestOnly:
		return "test-only"
	default:
		return fmt.Sprintf("Distribution(%d)", int(d))
	}
}

// Decided reports whether d is a member of the enum other than Unset — the
// question Base.Validate asks.
func (d Distribution) Decided() bool {
	switch d {
	case DistributionDefault, DistributionOptIn, DistributionTestOnly:
		return true
	default:
		return false
	}
}
