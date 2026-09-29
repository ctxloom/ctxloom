//go:build windows

package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// On a Windows host every Docker/Podman runtime names a host path in the
// Linux container by its drive letter; Host maps nothing.
func TestPathSeam_NilTargetIsHostMapper(t *testing.T) {
	assert.Equal(t, driveLetterMapper{}, newPathSeam(nil, nil).target)
	assert.Equal(t, driveLetterMapper{}, Docker{}.paths().target)
	assert.Equal(t, driveLetterMapper{}, Podman{}.paths().target)
	assert.Equal(t, identityMapper{}, Host{}.paths().target)
}
