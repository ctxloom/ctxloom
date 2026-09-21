package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestHomeHelloCapabilities_IsTheConfiguredAdvertisement: HomeConfig.Capabilities
// is what the Hello carries — empty advertises nothing, since the mailbox
// surface every runner has is not a capability.
func TestHomeHelloCapabilities_IsTheConfiguredAdvertisement(t *testing.T) {
	assert.Empty(t, (&Home{}).helloCapabilities())
	h := &Home{cfg: HomeConfig{Capabilities: coord.RunnerCapabilities(true)}}
	assert.Equal(t, coord.RunnerCapabilities(true), h.helloCapabilities())
}
