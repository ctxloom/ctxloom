// Package release names one bundle release: its name and its version.
package release

import (
	"github.com/Masterminds/semver/v3"
)

// Release identifies one published release of a bundle.
type Release struct {
	// Name is the bundle id. A tree served under any other id is not this
	// release.
	Name string
	// Version is strict semver.
	Version *semver.Version
}
