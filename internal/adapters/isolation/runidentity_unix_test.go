//go:build !windows

package isolation

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A POSIX host remaps to the launching user, so bind-mounted writes land
// owned by it.
func TestRunIdentity_IsTheLaunchingUser(t *testing.T) {
	uid, gid := runIdentity()
	assert.Equal(t, os.Getuid(), uid)
	assert.Equal(t, os.Getgid(), gid)
}

// Off Windows a podman machine's gvproxy lands host.containers.internal on
// this host's loopback, so the coordinator stays loopback-only.
func TestPodmanMachineRoute_IsTheAlias(t *testing.T) {
	r, err := podmanMachineRoute()
	assert.NoError(t, err)
	assert.Equal(t, hostRoute{dial: "host.containers.internal"}, r)
}
