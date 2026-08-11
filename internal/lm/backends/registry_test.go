// Backend registry tests verify that all supported LM backends are registered
// and accessible. The registry enables ctxloom to work with multiple AI coding
// assistants (Claude Code, Codex) through a unified interface.
package backends

import (
	"sort"
	"testing"

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

// List() must return a deterministic (sorted) order on its own — callers
// must not have to defensively sort a randomised Go map-iteration order
// themselves (e.g. shell-completion filtering, internal/cli/completion.go,
// does not sort today). Run repeatedly since a single run cannot distinguish
// "sorted" from "map iteration happened to come out sorted."
func TestRegistry_List_IsSorted(t *testing.T) {
	for i := 0; i < 20; i++ {
		names := List()
		require.True(t, sort.StringsAreSorted(names), "List() must return a sorted order; got %v", names)
	}
}

// BackendsWithSettings() is List()'s settings-scoped twin and must
// be equally deterministic.
func TestBackendsWithSettings_IsSorted(t *testing.T) {
	for i := 0; i < 20; i++ {
		names := BackendsWithSettings()
		require.True(t, sort.StringsAreSorted(names), "BackendsWithSettings() must return a sorted order; got %v", names)
	}
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

// IsAvailable used to collapse shellenv.Resolve's error to a bool, so
// "unregistered backend", "no default binary", and "binary not resolvable on
// PATH" were indistinguishable to any caller. AvailabilityOf is the new,
// diagnosable form IsAvailable is now a thin wrapper over — no existing
// exported signature changes.
func TestAvailabilityOf(t *testing.T) {
	t.Run("unregistered backend reports a reason", func(t *testing.T) {
		_, err := AvailabilityOf("nonexistent-backend")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nonexistent-backend")
	})

	t.Run("registered backend with no default binary reports a reason", func(t *testing.T) {
		_, err := AvailabilityOf("mock")
		require.Error(t, err)
	})

	t.Run("IsAvailable agrees with AvailabilityOf", func(t *testing.T) {
		_, err := AvailabilityOf("mock")
		assert.Equal(t, err == nil, IsAvailable("mock"))
	})
}

// TestDecodeLLMConfig verifies the backend config registry decodes a raw body
// into the backend's own typed struct, keyed solely by the type discriminator.
func TestDecodeLLMConfig(t *testing.T) {
	t.Run("claude-code decodes its fields", func(t *testing.T) {
		// "model" is deliberately included even though ClaudeConfig has no
		// Model field (deleted as dead): mapstructure ignores
		// unknown body keys, so a config carrying "model" alongside
		// "binary_path" must still decode cleanly.
		bc, err := DecodeLLMConfig("claude-code", map[string]interface{}{
			"model":       "haiku",
			"binary_path": "/custom/claude",
		})
		require.NoError(t, err)
		cc, ok := bc.(*claude.ClaudeConfig)
		require.True(t, ok, "decoder must yield *ClaudeConfig")
		assert.Equal(t, "/custom/claude", cc.BinaryPath)
	})

	t.Run("unknown type errors", func(t *testing.T) {
		_, err := DecodeLLMConfig("nope", nil)
		assert.Error(t, err)
	})
}

// A decode failure from a backend's own decoder must name the
// backend, so a multi-backend config load can attribute a mapstructure
// failure to the right entry instead of surfacing a bare, unattributed
// mapstructure error.
func TestDecodeLLMConfig_DecodeFailureNamesBackend(t *testing.T) {
	_, err := DecodeLLMConfig("claude-code", map[string]interface{}{
		"binary_path": map[string]interface{}{"nested": "not a string"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-code",
		"decode failure must name the backend it came from: got %q", err.Error())
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
			if name == "mock" || name == "acp" {
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

// TestDescriptorTable_ConfigDecodesToItsOwnType is what makes every backend's
// `cfg.(*XConfig); if !ok` arm unreachable in production, and is therefore the
// invariant that must be pinned rather than the arm hardened.
//
// Both production Configure call sites pair a backend with a config chosen by
// TYPE — ConfiguredBackend does Get(cfg.BackendType()), and cli's
// serveBackendConfig only decodes an entry whose EffectiveType() equals the
// backend name. So the ONLY way a backend can be handed a config it cannot read
// is a descriptor whose decodeConfig builds some other backend's struct. That
// mismatch is silent by construction: the wrong-typed config would be dropped
// whole, and the run would launch on defaults with every override ignored.
func TestDescriptorTable_ConfigDecodesToItsOwnType(t *testing.T) {
	for name, d := range descriptors {
		t.Run(name, func(t *testing.T) {
			cfg, err := d.decodeConfig(map[string]interface{}{})
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, name, cfg.BackendType(),
				"descriptor %q decodes a config that identifies as %q — the backend Get() resolves for it could never read it",
				name, cfg.BackendType())
		})
	}
}

// registerDescriptor must not silently overwrite an existing same-name
// entry — a duplicate registration is a programming error at init time (a
// future backend accidentally reusing a name), and the losing descriptor's
// writer/surfaces/exports would otherwise vanish with no signal.
func TestRegisterDescriptor_DuplicateNamePanics(t *testing.T) {
	const name = "u057-f25-dup-test"
	t.Cleanup(func() { UnregisterForTesting(name) })

	registerDescriptor(agentDescriptor{name: name})
	assert.Panics(t, func() {
		registerDescriptor(agentDescriptor{name: name})
	}, "a second registerDescriptor call for the same name must panic, not silently win")
}

// TestConfiguredBackend builds a backend from a typed config and applies it.
func TestConfiguredBackend(t *testing.T) {
	b := ConfiguredBackend(&claude.ClaudeConfig{BinaryPath: "/custom/claude"})
	require.NotNil(t, b)
	bp, ok := b.(BinaryPathProvider)
	require.True(t, ok)
	assert.Equal(t, "/custom/claude", bp.GetBinaryPath())
}
