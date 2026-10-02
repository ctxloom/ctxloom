package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Windows names compare without case: the OS's own Path is the base's PATH,
// and an env name spelt in any case keeps its variable.
func TestEnvHost_WindowsNamesFoldCase(t *testing.T) {
	h := EnvHost{Curated: true, Env: []string{"github_token"}}
	assert.True(t, h.Inherits("Path"))
	assert.True(t, h.Inherits("GITHUB_TOKEN"))
	assert.True(t, h.Inherits("xdg_config_home"))
	assert.False(t, h.Inherits("Unrelated_Secret"))
}
