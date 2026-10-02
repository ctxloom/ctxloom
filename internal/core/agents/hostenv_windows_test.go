package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Windows names compare without case: the OS's own Path is the base's PATH,
// and a passthrough spelt in any case keeps its variable.
func TestHostEnv_WindowsNamesFoldCase(t *testing.T) {
	h := HostEnv{Curated: true, Passthrough: []string{"github_token"}}
	assert.True(t, h.Inherits("Path"))
	assert.True(t, h.Inherits("GITHUB_TOKEN"))
	assert.True(t, h.Inherits("xdg_config_home"))
	assert.False(t, h.Inherits("Unrelated_Secret"))
}
