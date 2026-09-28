//go:build windows

package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Windows has no POSIX uid (os.Getuid is -1): the run drops to the image's
// own user instead of passing a uid the entrypoint cannot remap to.
func TestRunIdentity_IsTheImageUser(t *testing.T) {
	uid, gid := runIdentity()
	assert.Equal(t, imageUserID, uid)
	assert.Equal(t, imageUserID, gid)
}

// Under WSL the machine alias lands in the VM, so the route is this host's
// primary address — public, and saying why.
func TestPodmanMachineRoute_IsPublicWithReason(t *testing.T) {
	stubPrimary(t, "192.0.2.10")
	r, err := podmanMachineRoute()
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.10", r.dial)
	assert.True(t, r.listen.Public)
	assert.Contains(t, r.listen.Why, "WSL")
}
