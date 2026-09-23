// Package release is the signed statement a publisher makes about one bundle
// release: its name, its version, and which earlier versions it withdraws.
//
// It is a leaf, below both the content layer (which renders it into the signed
// manifest header) and the remote layer (which records the version floor in the
// lockfile), so neither has to reach the other to agree on what a release says.
package release

import (
	"errors"
	"fmt"

	"github.com/Masterminds/semver/v3"
)

// Release is what a publisher's signature over a bundle manifest asserts
// beyond the file hashes.
type Release struct {
	// Name is the bundle id the publisher signed. A tree served under any
	// other id is not this release.
	Name string
	// Version is strict semver; it is what the consumer's version floor is
	// measured in.
	Version *semver.Version
	// Retracts are earlier versions this release withdraws, sorted by Version.
	Retracts []Retraction
	// Withdrawn is the reason every version of the bundle is withdrawn; ""
	// when it is not.
	Withdrawn string
}

// Retraction withdraws one exact earlier version.
type Retraction struct {
	Version *semver.Version
	// Reason is a single line, for display only: nothing decides on it.
	Reason string
}

// Retracted reports whether this release withdraws version v, and why. A
// withdrawn bundle withdraws every version, including an unversioned pin; a
// retraction otherwise names one exact version and never a range.
func (r Release) Retracted(v *semver.Version) (bool, string) {
	if r.Withdrawn != "" {
		return true, r.Withdrawn
	}
	if v == nil {
		return false, ""
	}
	for _, x := range r.Retracts {
		if x.Version != nil && x.Version.Equal(v) {
			return true, x.Reason
		}
	}
	return false, ""
}

// ErrRollback refuses a pin whose signed version is below the one already
// recorded: moving a ref back to an older signed tree is the attack a version
// floor exists to stop.
var ErrRollback = errors.New("release: signed version is below the recorded floor")

// ErrSignatureDowngrade refuses a pin that recorded a signed version moving to
// content nobody this machine trusts signed. Without it, stripping the
// signature would be a way around the floor.
var ErrSignatureDowngrade = errors.New("release: previously signed content is no longer signed")

// CheckAdvance decides whether a pin whose recorded floor is floor may move to
// content whose signed version is next. A nil floor means nothing signed was
// ever recorded, so there is nothing to fall below; a nil next means the new
// content is unattested. An equal version is accepted: re-pinning the same
// release is not a rollback.
//
// allowDowngrade is the operator naming this one ref and accepting the lower
// version; the caller records it as the new floor.
func CheckAdvance(floor, next *semver.Version, allowDowngrade bool) error {
	if floor == nil || allowDowngrade {
		return nil
	}
	if next == nil {
		return fmt.Errorf("%w: %s was signed, the new content is not", ErrSignatureDowngrade, floor)
	}
	if next.LessThan(floor) {
		return fmt.Errorf("%w: %s is below %s", ErrRollback, next, floor)
	}
	return nil
}
