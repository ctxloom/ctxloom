package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChildVerbosity pins the env-only diagnostics knob: CTXLOOM_VERBOSE=1
// turns the child plugin/adapter stderr trail on at trace; anything else
// keeps the default-quiet launch.
func TestChildVerbosity(t *testing.T) {
	t.Setenv("CTXLOOM_VERBOSE", "")
	assert.Equal(t, 0, childVerbosity())
	t.Setenv("CTXLOOM_VERBOSE", "1")
	assert.Equal(t, 3, childVerbosity())
}
