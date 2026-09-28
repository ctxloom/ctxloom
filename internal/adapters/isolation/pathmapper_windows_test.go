//go:build windows

package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// On a Windows host every Docker/Podman runtime names a host path in the
// Linux container by its drive letter; Host maps nothing.
func TestRuntimeMapper_NilIsHostMapper(t *testing.T) {
	assert.Equal(t, driveLetterMapper{}, runtimeMapper(nil))
	assert.Equal(t, driveLetterMapper{}, Docker{}.mapper())
	assert.Equal(t, driveLetterMapper{}, Podman{}.mapper())
	assert.Equal(t, identityMapper{}, Host{}.mapper())
}
