//go:build windows

package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// On a Windows host every Docker/Podman runtime names a host path in the
// Linux container by its drive letter; Host maps nothing.
func TestPlacement_NilIsHostMapper(t *testing.T) {
	assert.Equal(t, driveLetterMapper{}, Docker{}.placement())
	assert.Equal(t, driveLetterMapper{}, Podman{}.placement())
	assert.Equal(t, identityMapper{}, Host{}.placement())
}
