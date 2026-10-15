package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// TestApplyLLMFields_RemovingTheLastNestedKeyRemovesItsObject: an empty value
// removes a key, and an object left empty goes with it, so an entry never
// carries `provider: {}`.
func TestApplyLLMFields_RemovingTheLastNestedKeyRemovesItsObject(t *testing.T) {
	entry := config.LLMConfig{Type: "opencode", Body: map[string]any{"model": "m", "model_options": map[string]any{"temperature": 0.6}}}
	got, err := applyLLMFields(entry, []string{"model_options.temperature="})
	require.NoError(t, err)
	assert.Equal(t, config.LLMConfig{Type: "opencode", Body: map[string]any{"model": "m"}}, got)

	got, err = applyLLMFields(config.LLMConfig{Type: "opencode", Body: map[string]any{"snapshot": true}}, []string{"snapshot=null", "absent.key="})
	require.NoError(t, err)
	assert.Nil(t, got.Body, "an entry body with nothing left is nil, not an empty map")
}

// TestApplyLLMFields_ReachesThePermissionsBlock: the permissions block is
// part of the entry as written, neutral and engine keys alike.
func TestApplyLLMFields_ReachesThePermissionsBlock(t *testing.T) {
	got, err := applyLLMFields(config.LLMConfig{Type: "opencode"}, []string{"permissions.approver=none", "permissions.mode=build"})
	require.NoError(t, err)
	assert.Equal(t, agents.LabelPermissions{NeutralPermissions: agents.NeutralPermissions{Approver: "none"}, Engine: map[string]any{"mode": "build"}}, got.Permissions)
}

// TestApplyLLMFields_RefusesMalformedAssignments: what cannot be applied is
// refused before the schema is ever asked.
func TestApplyLLMFields_RefusesMalformedAssignments(t *testing.T) {
	for assignment, want := range map[string]string{
		"context_window":            "<dotted.key>=<value>",
		"=1":                        "<dotted.key>=<value>",
		"provider..base_url=x":      "empty segment",
		"role=primary":              "not set from the CLI",
		"type=claude-code":          "--type",
		"context_window.inner=1":    "not an object",
		"model_options={unclosed: ": "not YAML",
	} {
		t.Run(assignment, func(t *testing.T) {
			_, err := applyLLMFields(config.LLMConfig{Type: "opencode", Body: map[string]any{"context_window": 1}}, []string{assignment})
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}
