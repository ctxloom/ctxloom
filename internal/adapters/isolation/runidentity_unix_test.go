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

// Off Windows no runtime VM is a WSL distro.
func TestMachineVMIsWSL_Off(t *testing.T) {
	assert.False(t, machineVMIsWSL)
}
