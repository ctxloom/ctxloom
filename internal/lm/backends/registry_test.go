// Backend registry tests verify that all supported LM backends are registered
// and accessible. The registry enables ctxloom to work with multiple AI coding
// assistants (Claude Code, Antigravity CLI, Codex) through a unified interface.
package backends

import (
	"sort"
	"testing"

	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Backend Registration Tests
// =============================================================================
// All built-in backends must be registered and retrievable by name.

func TestRegistry_GetBuiltinBackends(t *testing.T) {
	// Every supported backend must be registered for `ctxloom run` to work
	builtinNames := []string{
		"claude-code",
		"antigravity",
		"codex",
		"mock",
	}

	for _, name := range builtinNames {
		t.Run(name, func(t *testing.T) {
			backend := Get(name)
			assert.NotNil(t, backend)
			assert.Equal(t, name, backend.Name())
		})
	}
}

func TestRegistry_GetNonExistent(t *testing.T) {
	// Unknown backends return nil - enables graceful error handling
	backend := Get("nonexistent-backend")
	assert.Nil(t, backend)
}

func TestRegistry_Exists(t *testing.T) {
	// Exists check enables validation before attempting to run
	assert.True(t, Exists("claude-code"))
	assert.True(t, Exists("mock"))
	assert.False(t, Exists("nonexistent"))
}

func TestRegistry_List(t *testing.T) {
	// List enables help output and tab completion
	names := List()
	assert.GreaterOrEqual(t, len(names), 4) // At least the builtin backends

	sort.Strings(names)
	assert.Contains(t, names, "claude-code")
	assert.Contains(t, names, "mock")
}

func TestGetDefaultBinary(t *testing.T) {
	t.Run("returns binary for registered backend", func(t *testing.T) {
		// Mock backend returns empty string since it has no real binary
		binary := GetDefaultBinary("mock")
		assert.Equal(t, "", binary)
	})

	t.Run("returns empty for non-existent backend", func(t *testing.T) {
		binary := GetDefaultBinary("nonexistent")
		assert.Equal(t, "", binary)
	})
}

func TestIsAvailable(t *testing.T) {
	t.Run("mock backend is not available (no real binary)", func(t *testing.T) {
		// Mock backend doesn't have a real binary path, so it won't be "available"
		available := IsAvailable("mock")
		assert.False(t, available)
	})

	t.Run("non-existent backend is not available", func(t *testing.T) {
		available := IsAvailable("nonexistent-backend")
		assert.False(t, available)
	})
}

// TestDecodeLLMConfig verifies the backend config registry decodes a raw body
// into the backend's own typed struct, keyed solely by the type discriminator.
func TestDecodeLLMConfig(t *testing.T) {
	t.Run("claude-code decodes its fields", func(t *testing.T) {
		bc, err := DecodeLLMConfig("claude-code", map[string]interface{}{
			"model":       "haiku",
			"binary_path": "/custom/claude",
		})
		require.NoError(t, err)
		cc, ok := bc.(*claude.ClaudeConfig)
		require.True(t, ok, "decoder must yield *ClaudeConfig")
		assert.Equal(t, "haiku", cc.Model)
		assert.Equal(t, "/custom/claude", cc.BinaryPath)
	})

	t.Run("antigravity decodes its fields", func(t *testing.T) {
		bc, err := DecodeLLMConfig("antigravity", map[string]interface{}{
			"model":       "gemini-3-pro",
			"binary_path": "/custom/agy",
		})
		require.NoError(t, err)
		ac, ok := bc.(*antigravity.AntigravityConfig)
		require.True(t, ok, "decoder must yield *AntigravityConfig")
		assert.Equal(t, "gemini-3-pro", ac.Model)
		assert.Equal(t, "/custom/agy", ac.BinaryPath)
	})

	t.Run("unknown type errors", func(t *testing.T) {
		_, err := DecodeLLMConfig("nope", nil)
		assert.Error(t, err)
	})
}

// TestDescriptorTable_Invariants pins the descriptor registry's shape: every
// built-in agent is registered as ONE complete descriptor (backend ctor +
// config decoder + settings writer + surface builder + command-export mapper),
// keyed by the name its module's Name() reports. The mock backend is the
// deliberate exception — it registers only backend+config (no settings, no
// surfaces, no exports). A descriptor that loses a capability field silently
// degrades that backend, so this must fail loudly.
func TestDescriptorTable_Invariants(t *testing.T) {
	require.NotEmpty(t, descriptors)
	for name, d := range descriptors {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, name, d.name, "descriptor keyed under a different name than it carries")
			require.NotNil(t, d.newBackend, "every descriptor must construct a backend")
			require.NotNil(t, d.decodeConfig, "every descriptor must decode its config")
			assert.Equal(t, name, d.newBackend().Name(),
				"registry name must match the module's Name()")

			// Deliberate exemptions: mock is the test double; acp is the GENERIC
			// ACP client, which has no native config format to write — the known
			// agents' ACP paths ride their own descriptors (kiro/codex Chat
			// delegates to the acp driver), so materialization stays with the
			// target's writer, never this descriptor. (acp still registers a
			// newSurfaces that yields an EmptySurfaceSet so BuildSurfaces is total.)
			// opencode is the slice-1 HOST-only chat spine: it materializes no
			// native config yet (opencode reads .claude/skills/ and CLAUDE.md
			// natively, and the model rides a project-local opencode.json written
			// on the chat path) — its settings writer / command exports arrive in a
			// later slice, at which point this exemption is removed.
			if name == "mock" || name == "acp" || name == "opencode" {
				assert.Nil(t, d.newWriter, "%s must not gain settings support silently", name)
				assert.Nil(t, d.exports, "%s must not gain command export silently", name)
				return
			}
			assert.NotNil(t, d.newWriter, "backend must have a settings writer")
			assert.NotNil(t, d.newSurfaces, "backend must build a surface set")
			assert.NotNil(t, d.exports, "backend must have a command-export mapper")
		})
	}
}

// TestConfiguredBackend builds a backend from a typed config and applies it.
func TestConfiguredBackend(t *testing.T) {
	b := ConfiguredBackend(&claude.ClaudeConfig{BinaryPath: "/custom/claude"})
	require.NotNil(t, b)
	bp, ok := b.(BinaryPathProvider)
	require.True(t, ok)
	assert.Equal(t, "/custom/claude", bp.GetBinaryPath())
}
