package isolation

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOwnerPIDNamespace_IsThisProcesssPIDNamespace: on Linux the namespace a
// pid is read in is the kernel's own name for it.
func TestOwnerPIDNamespace_IsThisProcesssPIDNamespace(t *testing.T) {
	want, err := os.Readlink("/proc/self/ns/pid")
	require.NoError(t, err)
	assert.Equal(t, want, ownerPIDNamespace())
	assert.NotEmpty(t, ownerPIDNamespace())
}
