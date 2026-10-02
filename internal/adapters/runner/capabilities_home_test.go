package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHomeHelloCapabilities_IsTheConfiguredAdvertisement: HomeConfig.Capabilities
// is what the Hello carries — empty advertises nothing.
func TestHomeHelloCapabilities_IsTheConfiguredAdvertisement(t *testing.T) {
	assert.Empty(t, (&Home{}).helloCapabilities())
	adv := []string{"test_capability"}
	h := &Home{cfg: HomeConfig{Capabilities: adv}}
	assert.Equal(t, adv, h.helloCapabilities())
}
