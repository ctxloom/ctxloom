package agent

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestMergeHooksConfig_NilInputs(t *testing.T) {
	t.Run("nil dest does nothing", func(t *testing.T) {
		src := &config.HooksConfig{
			Unified: config.UnifiedHooks{
				PreTool: []config.Hook{{Command: "test"}},
			},
		}
		// Should not panic
		mergeHooksConfig(nil, src)
	})

	t.Run("nil src does nothing", func(t *testing.T) {
		dest := &config.HooksConfig{}
		mergeHooksConfig(dest, nil)
		assert.Empty(t, dest.Unified.PreTool)
	})

	t.Run("both nil does nothing", func(t *testing.T) {
		mergeHooksConfig(nil, nil)
	})
}

func TestMergeHooksConfig_UnifiedHooks(t *testing.T) {
	dest := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool: []config.Hook{{Command: "existing-pre"}},
		},
	}
	src := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool:      []config.Hook{{Command: "new-pre"}},
			PostTool:     []config.Hook{{Command: "new-post"}},
			SessionStart: []config.Hook{{Command: "session-start"}},
			SessionEnd:   []config.Hook{{Command: "session-end"}},
			PreShell:     []config.Hook{{Command: "pre-shell"}},
			PostFileEdit: []config.Hook{{Command: "post-edit"}},
		},
	}

	mergeHooksConfig(dest, src)

	assert.Len(t, dest.Unified.PreTool, 2)
	assert.Equal(t, "existing-pre", dest.Unified.PreTool[0].Command)
	assert.Equal(t, "new-pre", dest.Unified.PreTool[1].Command)
	assert.Len(t, dest.Unified.PostTool, 1)
	assert.Len(t, dest.Unified.SessionStart, 1)
	assert.Len(t, dest.Unified.SessionEnd, 1)
	assert.Len(t, dest.Unified.PreShell, 1)
	assert.Len(t, dest.Unified.PostFileEdit, 1)
}

func TestMergeHooksConfig_PluginSpecificHooks(t *testing.T) {
	t.Run("creates plugin map if nil", func(t *testing.T) {
		dest := &config.HooksConfig{}
		src := &config.HooksConfig{
			Plugins: map[string]config.BackendHooks{
				"claude-code": {
					"PreTool": []config.Hook{{Command: "claude-hook"}},
				},
			},
		}

		mergeHooksConfig(dest, src)

		assert.NotNil(t, dest.Plugins)
		assert.Len(t, dest.Plugins["claude-code"]["PreTool"], 1)
	})

	t.Run("merges into existing plugins", func(t *testing.T) {
		dest := &config.HooksConfig{
			Plugins: map[string]config.BackendHooks{
				"claude-code": {
					"PreTool": []config.Hook{{Command: "existing"}},
				},
			},
		}
		src := &config.HooksConfig{
			Plugins: map[string]config.BackendHooks{
				"claude-code": {
					"PreTool":  []config.Hook{{Command: "new"}},
					"PostTool": []config.Hook{{Command: "post"}},
				},
				"gemini": {
					"PreTool": []config.Hook{{Command: "gemini-hook"}},
				},
			},
		}

		mergeHooksConfig(dest, src)

		assert.Len(t, dest.Plugins["claude-code"]["PreTool"], 2)
		assert.Len(t, dest.Plugins["claude-code"]["PostTool"], 1)
		assert.Len(t, dest.Plugins["gemini"]["PreTool"], 1)
	})
}
