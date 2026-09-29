//go:build !windows

package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// On a POSIX host every Docker/Podman runtime shares the host's path
// namespace (natively, or through a VM that shares it at the same path), so
// an unset mapper is identity.
func TestPathSeam_NilTargetIsHostMapper(t *testing.T) {
	assert.Equal(t, identityMapper{}, newPathSeam(nil, nil).target)
	assert.Equal(t, identityMapper{}, Docker{}.paths().target)
	assert.Equal(t, identityMapper{}, Podman{}.paths().target)
}

// On a POSIX host the unmapped runtime renders the identical-path project
// mount and WorkDir byte-for-byte.
func TestBuildRunnerSpec_IdentityMapper_Unchanged(t *testing.T) {
	spec := runnerSpecFor(Docker{}, "mock", "/proj", nil, nil)
	assert.Equal(t, "/proj", spec.WorkDir)
	assert.Contains(t, spec.Mounts, mount{Host: "/proj", Container: "/proj"})
	assert.Equal(t, mount{Host: "/repo/.git", Container: "/repo/.git"}, exposedMapped(t, Docker{}, "/repo/.git", false))
}
