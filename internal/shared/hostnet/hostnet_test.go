package hostnet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsLocalAddr: loopback is always one of the host's own addresses; a
// TEST-NET-1 address never is, and neither is something that does not parse.
func TestIsLocalAddr(t *testing.T) {
	local, err := IsLocalAddr("127.0.0.1")
	require.NoError(t, err)
	assert.True(t, local)

	local, err = IsLocalAddr("192.0.2.1")
	require.NoError(t, err)
	assert.False(t, local)

	local, err = IsLocalAddr("not-an-ip")
	require.NoError(t, err)
	assert.False(t, local)
}
