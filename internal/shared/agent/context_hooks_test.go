package agent

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NewContextInjectionHooks discarded ReadContextFile's error entirely
// (`content, _ := ReadContextFile(...)`), so any read failure —
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

// A nil dest means the caller has nowhere to put the merged hooks, and the
// signature gives MergeHooksConfig no way to refuse — so it returned silently
// and the ENTIRE source hook set vanished. That is this project's signature
// failure shape applied to the hook surface: the session launches with none of
// the configured hooks and nothing anywhere says so. The drop is still a drop
// (there is no destination to write to), but it must be named.
func TestMergeHooksConfig_NilDestNamesTheDroppedHooks(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	src := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool:      []wire.Hook{{Command: "pre"}},
			SessionStart: []wire.Hook{{Command: "start"}},
		},
		Plugins: map[string]wire.BackendHooks{
			"claude": {"PreToolUse": []wire.Hook{{Command: "plugin"}}},
		},
	}
	// The fixture must actually carry hooks, or the silence under test is
	// legitimate and the pin proves nothing.
	require.NotEmpty(t, src.Unified.PreTool)
	require.NotEmpty(t, src.Plugins)

	assert.NotPanics(t, func() { MergeHooksConfig(nil, src) })

	out := buf.String()
	assert.Contains(t, out, "warning:", "a whole hook set going missing must reach the diagnostic channel")
	assert.Contains(t, out, "3", "the warning must say HOW MANY hooks were dropped")
}

// Dropping an EMPTY source into a nil dest loses nothing, so it must stay
// silent — a warning there would be noise on every no-op merge.
func TestMergeHooksConfig_NilDestWithNothingToLoseStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	MergeHooksConfig(nil, &wire.HooksConfig{})
	MergeHooksConfig(nil, nil)
	MergeHooksConfig(&wire.HooksConfig{}, nil)

	assert.Empty(t, buf.String(), "a merge that loses nothing must not warn")
}

// TestMergeHooksConfig_UnifiedHalfMatchesWireAppend is the parity test
// required across BOTH implementations of the same merge. The hooks vocabulary
// lives in wire and carries UnifiedHooks.Append; MergeHooksConfig, one layer
// up, re-spelled the same six appends by hand, and the plugin half had no wire
// primitive at all. Two spellings of one rule drift in one direction only —
// a seventh event added to UnifiedHooks reaches Append and is silently dropped
// by the copy.
//
// The two bodies are VERBATIM identical today, so this parity check cannot be
// red; that is the point of running it BEFORE the collapse. Its job is to pin
// the shared behaviour so the collapse is provably behaviour-preserving, at the
// public seam (MergeHooksConfig) that the collapse does not move.
func TestMergeHooksConfig_UnifiedHalfMatchesWireAppend(t *testing.T) {
	existing := wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "existing-pre"}}}
	src := wire.UnifiedHooks{
		PreTool:      []wire.Hook{{Command: "new-pre"}},
		PostTool:     []wire.Hook{{Command: "new-post"}},
		SessionStart: []wire.Hook{{Command: "session-start"}},
		SessionEnd:   []wire.Hook{{Command: "session-end"}},
		PreShell:     []wire.Hook{{Command: "pre-shell"}},
		PostFileEdit: []wire.Hook{{Command: "post-edit"}},
	}

	viaMerge := &wire.HooksConfig{Unified: existing}
	MergeHooksConfig(viaMerge, &wire.HooksConfig{Unified: src})

	viaAppend := existing
	viaAppend.Append(src)

	assert.Equal(t, viaAppend, viaMerge.Unified,
		"the hand-written unified merge and wire's own Append disagree")
}

// TestCountHooks_CountsEveryUnifiedEvent keeps the drop-report honest. countHooks
// is a hand-written sum over UnifiedHooks' fields and its only consumer is the
// "N hooks lost" warning — an event missing from the sum makes a whole hook set
// vanish while the warning under-reports the loss, or reports none at all when
// the lost set was entirely that event.
func TestCountHooks_CountsEveryUnifiedEvent(t *testing.T) {
	typ := reflect.TypeOf(wire.UnifiedHooks{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			var u wire.UnifiedHooks
			reflect.ValueOf(&u).Elem().Field(i).Set(reflect.ValueOf([]wire.Hook{{Command: "x"}}))
			assert.Equal(t, 1, countHooks(&wire.HooksConfig{Unified: u}),
				"a lone %s hook must be counted, or the loss it represents is reported as nothing", name)
		})
	}
}
