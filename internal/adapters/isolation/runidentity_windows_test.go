//go:build windows

package isolation

import (
	"context"
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

// Under WSL the machine alias lands in the VM, so a podman machine's route
// is this host's primary address — public, and saying why — while Docker
// Desktop keeps its own host proxy's alias.
func TestReachRoute_WindowsMachines(t *testing.T) {
	stubPrimary(t, "192.0.2.10")
	r, err := Podman{rootless: true, rootlessNet: "pasta"}.reachRoute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.10", r.dial)
	assert.True(t, r.listen.Public)
	assert.Contains(t, r.listen.Why, "WSL")

	d, err := Docker{}.reachRoute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, hostRoute{dial: "host.docker.internal"}, d)
}
