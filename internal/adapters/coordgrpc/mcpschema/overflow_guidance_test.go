package mcpschema

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestOverflowGuidance_NamesTheLiveCapAndCue: the tools whose text a reader
// sees inline tell the writer where the fold is. The cap and the cue are the
// coordinator's (coord.MaxInlineBodyBytes, coord.OverflowMarkerPhrase), so a
// change to either fails here instead of leaving the description lying.
func TestOverflowGuidance_NamesTheLiveCapAndCue(t *testing.T) {
	tools, err := Tools()
	require.NoError(t, err)
	byName := map[string]ToolSpec{}
	for _, s := range tools {
		byName[s.Name] = s
	}
	cap := fmt.Sprintf("Only the first %d KiB is shown inline", coord.MaxInlineBodyBytes>>10)
	cue := fmt.Sprintf("%q", coord.OverflowMarkerPhrase)
	for _, name := range []string{ToolAgentSend, ToolAgentReport} {
		spec, ok := byName[name]
		require.True(t, ok, name)
		assert.Contains(t, spec.Description, "Lead with the most important point and the evidence for it.", name)
		assert.Contains(t, spec.Description, cap, name)
		assert.Contains(t, spec.Description, cue, name)
	}
}
