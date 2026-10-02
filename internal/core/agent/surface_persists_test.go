package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shared no-op handle is what the project-surface deliveries return: a
// project surface SURVIVES the run that delivered it. Its CONTRACT is the thing that must not drift: a
// future delivery that wants real teardown must be per-session SCRATCH, which
// nothing reconciles and which `clean` cannot see, and must not reach for this.
func TestSurfacePersistsAfterExit_ReversesNothing(t *testing.T) {
	require.NotNil(t, SurfacePersistsAfterExit)
	assert.NoError(t, SurfacePersistsAfterExit.Cleanup(),
		"the shared project-surface handle must be a successful no-op")
}
