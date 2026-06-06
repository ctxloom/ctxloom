package agent

import (
	"testing"

	"github.com/ctxloom/shared/wire"
	"github.com/stretchr/testify/assert"
)

func TestMergeHooksConfig_NilInputs(t *testing.T) {
	t.Run("nil dest does nothing", func(t *testing.T) {
		src := &wire.HooksConfig{
			Unified: wire.UnifiedHooks{
				PreTool: []wire.Hook{{Command: "test"}},
			},
		}
		// Should not panic
		MergeHooksConfig(nil, src)
	})

	t.Run("nil src does nothing", func(t *testing.T) {
		dest := &wire.HooksConfig{}
		MergeHooksConfig(dest, nil)
		assert.Empty(t, dest.Unified.PreTool)
	})

	t.Run("both nil does nothing", func(t *testing.T) {
		MergeHooksConfig(nil, nil)
	})
}

func TestMergeHooksConfig_UnifiedHooks(t *testing.T) {
	dest := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "existing-pre"}},
		},
	}
	src := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool:      []wire.Hook{{Command: "new-pre"}},
			PostTool:     []wire.Hook{{Command: "new-post"}},
			SessionStart: []wire.Hook{{Command: "session-start"}},
			SessionEnd:   []wire.Hook{{Command: "session-end"}},
			PreShell:     []wire.Hook{{Command: "pre-shell"}},
			PostFileEdit: []wire.Hook{{Command: "post-edit"}},
		},
	}

	MergeHooksConfig(dest, src)

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
		dest := &wire.HooksConfig{}
		src := &wire.HooksConfig{
			Plugins: map[string]wire.BackendHooks{
				"claude-code": {
					"PreTool": []wire.Hook{{Command: "claude-hook"}},
				},
			},
		}

		MergeHooksConfig(dest, src)

		assert.NotNil(t, dest.Plugins)
		assert.Len(t, dest.Plugins["claude-code"]["PreTool"], 1)
	})

	t.Run("merges into existing plugins", func(t *testing.T) {
		dest := &wire.HooksConfig{
			Plugins: map[string]wire.BackendHooks{
				"claude-code": {
					"PreTool": []wire.Hook{{Command: "existing"}},
				},
			},
		}
		src := &wire.HooksConfig{
			Plugins: map[string]wire.BackendHooks{
				"claude-code": {
					"PreTool":  []wire.Hook{{Command: "new"}},
					"PostTool": []wire.Hook{{Command: "post"}},
				},
				"gemini": {
					"PreTool": []wire.Hook{{Command: "gemini-hook"}},
				},
			},
		}

		MergeHooksConfig(dest, src)

		assert.Len(t, dest.Plugins["claude-code"]["PreTool"], 2)
		assert.Len(t, dest.Plugins["claude-code"]["PostTool"], 1)
		assert.Len(t, dest.Plugins["gemini"]["PreTool"], 1)
	})
}
