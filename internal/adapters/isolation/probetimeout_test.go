package isolation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestProbeTimeouts_AreThirtySecondsAndRuntimeNoStricter pins the owner's
// 2026-10-12 ruling (#32): both container probe bounds are 30s, raised
// together, and the runtime-selection probe never gives up before the
// shared-FS probe it gates would have.
func TestProbeTimeouts_AreThirtySecondsAndRuntimeNoStricter(t *testing.T) {
	assert.Equal(t, 30*time.Second, sharedFSProbeTimeout)
	assert.Equal(t, 30*time.Second, runtimeProbeTimeout)
	assert.GreaterOrEqual(t, runtimeProbeTimeout, sharedFSProbeTimeout,
		"the runtime probe must be no stricter than the shared-FS probe")
}
