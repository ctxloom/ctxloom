package confload

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOverrideSource_String pins the channel names diagnostics carry.
func TestOverrideSource_String(t *testing.T) {
	assert.Equal(t, "env", SourceEnv.String())
	assert.Equal(t, "flag", SourceFlag.String())
	assert.Equal(t, "unknown", OverrideSource(99).String())
}
