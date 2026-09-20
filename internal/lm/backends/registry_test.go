// Backend registry tests verify that all supported LM backends are registered
// and accessible. The registry enables ctxloom to work with multiple AI coding
// assistants (Claude Code, Codex) through a unified interface.
package backends

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// =============================================================================
// Backend Registration Tests
// =============================================================================
// All built-in backends must be registered and retrievable by name.

func TestRegistry_GetBuiltinBackends(t *testing.T) {
	// Every supported backend must be registered for `ctxloom run` to work
	builtinNames := []string{
		"claude-code",
		"mock",
	}

	for _, name := range builtinNames {
		t.Run(name, func(t *testing.T) {
			backend := Get(name)
			// require, not assert: Get returns a nil INTERFACE for an
			// unregistered name, so an assert here continues into
			// backend.Name() and panics — which aborts the whole test binary
			// and silently cancels every test declared after this one. That is
			// exactly how a stale roster here hid unrelated failures in this
			// package rather than reporting one.
			require.NotNil(t, backend, "%s must be registered", name)
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
	// List enables help output and tab completion.
	//
	// The expectation is DERIVED from the registry, not a hard-coded floor.
	// The floor this replaces ("at least 4") was a census of the engines that
	// happened to exist the day it was written: it went stale the moment the
	// roster shrank, and until then it constrained nothing about List() that
	// the roster size did not already decide.
	names := List()

	var want []string
	for name := range records {
		want = append(want, name)
	}
	sort.Strings(want)
	require.NotEmpty(t, want, "no descriptor is registered, so this comparison would be trivially satisfied")
	assert.Equal(t, want, names,
		"List() must name every registered descriptor, in sorted order, and only those")

	// Concrete anchors, so both sides going empty together cannot pass.
	assert.Contains(t, names, "claude-code")
	assert.Contains(t, names, "mock")
}

// List() must return a deterministic (sorted) order on its own — callers
// must not have to defensively sort a randomised Go map-iteration order
// themselves (e.g. shell-completion filtering, internal/adapters/cli/completion.go,
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

// TestDescriptorTable_Invariants pins the registry's shape: every registered
// descriptor is keyed by the name its backend's Name() reports, and every
// shipped engine carries settings, surfaces and command export. mock is NOT
// exempt: it is a complete engine with no real model behind it.
func TestDescriptorTable_Invariants(t *testing.T) {
	require.NotEmpty(t, records)
	for name, r := range records {
		d := &r.host
		t.Run(name, func(t *testing.T) {
			require.Equal(t, name, string(d.Engine), "descriptor keyed under a different name than it carries")
			assert.Equal(t, name, d.NewBackend(RunLaunchSpec).Name(),
				"registry name must match the module's Name()")
			_, hasWriter := d.SettingsWriter.Get()
			assert.True(t, hasWriter, "backend must have a settings writer")
			assert.NotEmpty(t, d.Surfaces, "backend must declare its surfaces")
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
// is a descriptor whose NewConfig builds some other backend's struct. That
// mismatch is silent by construction: the wrong-typed config would be dropped
// whole, and the run would launch on defaults with every override ignored.
func TestDescriptorTable_ConfigDecodesToItsOwnType(t *testing.T) {
	for name := range records {
		t.Run(name, func(t *testing.T) {
			cfg, err := DecodeLLMConfig(name, map[string]interface{}{})
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, name, cfg.BackendType(),
				"descriptor %q decodes a config that identifies as %q — the backend Get() resolves for it could never read it",
				name, cfg.BackendType())
		})
	}
}

// Register must not silently overwrite an existing same-name entry — a
// duplicate registration is a programming error (a future backend
// accidentally reusing a name), and the losing descriptor's
// writer/surfaces/exports would otherwise vanish with no signal. It is an
// ERROR, not a panic: registration runs from a composition root that can
// surface it.
func TestRegister_DuplicateNameIsAnError(t *testing.T) {
	const name = "u057-f25-dup-test"
	t.Cleanup(func() { UnregisterForTesting(name) })

	require.NoError(t, registerFixtures(enginefixture.Hosting(name)))
	err := registerFixtures(enginefixture.Hosting(name))
	require.Error(t, err, "a second Register call for the same name must be refused, not silently win")
	assert.Contains(t, err.Error(), name)
}

// A batch with one bad descriptor installs NOTHING: the good ones are not
// half-registered around the refusal.
func TestRegister_BatchIsAllOrNothing(t *testing.T) {
	const good = "u057-batch-good"
	t.Cleanup(func() { UnregisterForTesting(good) })
	bad := enginefixture.Hosting("u057-batch-bad")
	bad.NewBackend = nil

	require.Error(t, registerFixtures(enginefixture.Hosting(good), bad))
	assert.False(t, Exists(good), "the valid descriptor must not be installed when its batch is refused")
}

// A refused descriptor's error names the engine and the slot, so the
// composition root's message says what to fix.
func TestRegister_UndeclaredSlotIsRefusedByName(t *testing.T) {
	d := enginefixture.Hosting("u057-undeclared")
	d.VersionCommand = engine.Declared[engineversion.Command]{}
	err := registerFixtures(d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "u057-undeclared")
	assert.Contains(t, err.Error(), "VersionCommand")
	assert.False(t, Exists("u057-undeclared"))
}

// TestConfiguredBackend builds a backend from a typed config and applies it.
func TestConfiguredBackend(t *testing.T) {
	b := ConfiguredBackend(&claude.ClaudeConfig{BinaryPath: "/custom/claude"})
	require.NotNil(t, b)
	bp, ok := b.(BinaryPathProvider)
	require.True(t, ok)
	assert.Equal(t, "/custom/claude", bp.GetBinaryPath())
}

// Register pairs every hosting record with a composed kind and every
// composed kind with a hosting record: an engine can be neither declared
// here and unrunnable, nor runnable and undeclared.
func TestRegister_PairsEveryHostingWithItsKind(t *testing.T) {
	const name = "u6b-unpaired"
	t.Cleanup(func() { UnregisterForTesting(name) })

	err := Register(enginefixture.RegistryOf(), enginefixture.Hosting(name))
	require.Error(t, err, "a hosting record for a kind nobody composed is refused")
	assert.Contains(t, err.Error(), name)
	assert.False(t, Exists(name))

	err = Register(enginefixture.RegistryOf(enginefixture.Kind(name)))
	require.Error(t, err, "a composed kind with no hosting record is refused")
	assert.Contains(t, err.Error(), name)
	assert.False(t, Exists(name))
}

// The declarative facts are read off the KIND: the distribution, the
// read-only plan, the model aliases, and the Definition itself; Engines
// returns every registered kind.
func TestRegistry_DeclarativeFactsAreTheKinds(t *testing.T) {
	const name = "u6b-facts"
	t.Cleanup(func() { UnregisterForTesting(name) })
	kind := enginefixture.Kind(name, mock.WithDistribution(engine.DistributionOptIn), func(m *mock.Mock) {
		m.Permissions.ReadOnlyPlan = true
		m.ModelAliases = map[string]string{"fast": "fixture-fast-1"}
	})
	require.NoError(t, Register(enginefixture.RegistryOf(kind), enginefixture.Hosting(name)))

	def, ok := Definition(name)
	require.True(t, ok)
	assert.Equal(t, engine.Name(name), def.Name)
	assert.Equal(t, engine.DistributionOptIn, DistributionFor(name))
	assert.False(t, IsTestOnly(name))
	assert.True(t, EnforcesReadOnlyPlan(name))
	resolved, ok := ResolveModelFor(name, "fast")
	require.True(t, ok)
	assert.Equal(t, "fixture-fast-1", resolved)
	resolved, ok = ResolveModelFor(name, "other")
	require.True(t, ok)
	assert.Equal(t, "other", resolved, "an unaliased model passes through")

	_, ok = Engines().Lookup(name)
	assert.True(t, ok, "Engines lists every registered kind")
	_, ok = Definition("never-registered")
	assert.False(t, ok)
	assert.False(t, EnforcesReadOnlyPlan("never-registered"))
}
