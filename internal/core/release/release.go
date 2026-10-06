// Package release is the signed statement a publisher makes about one bundle
// release: its name and its version.
//
// It is a leaf, below both the content layer (which renders it into the signed
// manifest header) and the remote layer (which reports what a verified tree
// was signed as), so neither has to reach the other to agree on what a release
// says.
package release

import (
	"github.com/Masterminds/semver/v3"
)

// Release is what a publisher's signature over a bundle manifest asserts
// beyond the file hashes.
type Release struct {
	// Name is the bundle id the publisher signed. A tree served under any
	// other id is not this release.
	Name string
	// Version is strict semver.
	Version *semver.Version
}
