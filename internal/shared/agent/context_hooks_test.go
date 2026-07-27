package agent

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U100-F01: NewContextInjectionHooks discarded ReadContextFile's error
// entirely (`content, _ := ReadContextFile(...)`), so any read failure —
// permissions, a reaped cache file, an unexpected I/O error — silently
// collapsed what should be N ordered chunk hooks down to one whole-content
// hook, reintroducing exactly the truncation ContextChunkMaxChars exists to
// prevent, with zero diagnostic. The fallback-to-one-hook degrade is still
// correct (best-effort, and the runtime hook re-reads the file itself at
// fire time), but the read failure must not be silent.
func TestNewContextInjectionHooks_ReadFailureIsWarned(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	tmpDir := t.TempDir()
	hooks := NewContextInjectionHooks("never-written-hash", tmpDir)
	require.Len(t, hooks, 1, "a read failure still degrades to the single whole-content hook")
	assert.Contains(t, buf.String(), "never-written-hash",
		"a context-file read failure while building the injection hook(s) must be warned, not silently swallowed")
}

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
				"antigravity": {
					"PreTool": []wire.Hook{{Command: "antigravity-hook"}},
				},
			},
		}

		MergeHooksConfig(dest, src)

		assert.Len(t, dest.Plugins["claude-code"]["PreTool"], 2)
		assert.Len(t, dest.Plugins["claude-code"]["PostTool"], 1)
		assert.Len(t, dest.Plugins["antigravity"]["PreTool"], 1)
	})
}
