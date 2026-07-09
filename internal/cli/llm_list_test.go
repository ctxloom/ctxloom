// `llm list --format json` feeds frontends (the VSCode companion's model
// picker) the labels `-l`/`--llm` accepts, so the entry construction — label
// order preserved, exactly the primary label marked default — must hold.
package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLLMListEntries_MarksDefaultLabel(t *testing.T) {
	entries := llmListEntries([]string{"antigravity", "claude-code", "codex"}, "claude-code")

	assert.Equal(t, []llmEntry{
		{Label: "antigravity"},
		{Label: "claude-code", Default: true},
		{Label: "codex"},
	}, entries)
}

func TestLLMListEntries_NoDefaultWhenLabelEmpty(t *testing.T) {
	// Config-unavailable degradation: names enumerate, nothing is default.
	entries := llmListEntries([]string{"claude-code", "codex"}, "")

	for _, e := range entries {
		assert.False(t, e.Default, "no entry should be default without a primary label")
	}
}
