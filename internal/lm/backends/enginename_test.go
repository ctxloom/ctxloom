package backends

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
)

// TestRegistryLookups_ResolveTheRegisteredName pins every lookup a caller can
// enter by to the one spelling an engine has: its registered name.
func TestRegistryLookups_ResolveTheRegisteredName(t *testing.T) {
	name := claude.EngineName
	b := Get(name)
	require.NotNil(t, b, "Get(%q) returned nil", name)
	assert.Equal(t, name, b.Name())
	assert.True(t, Exists(name))
	assert.True(t, EnforcesReadOnlyPlan(name))

	cfg, err := DecodeLLMConfig(name, map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, name, cfg.BackendType())

	assert.NotNil(t, GetSettingsWriter(name, afero.NewMemMapFs()))

	set, err := SurfacesFor(name)
	require.NoError(t, err)
	assert.NotNil(t, set)

	_, ok := VersionCommandFor(name)
	require.True(t, ok)
}

// TestRegistryLookups_RefuseEveryOtherSpelling: there is no alias table, no
// case folding and no prefix matching. A spelling that is not exactly a
// registered name is an unknown engine at every lookup — the retired short
// spellings of claude-code ("claude", "claudecode") and its case variants
// refuse exactly as a typo or a removed engine does, nothing is rounded to a
// real backend, and nothing answers with a default.
func TestRegistryLookups_RefuseEveryOtherSpelling(t *testing.T) {
	// "antigravity" is a RETIRED engine name, kept deliberately: a stale config
	// naming a backend this build has removed must be refused, not silently
	// accepted. It is a rejection case, not a live roster member.
	for _, name := range []string{
		"claude", "claudecode", "CLAUDE", "CLAUDE-CODE", "Claude-Code",
		"totally-bogus", "clau", "claude-", "", "antigravity", "CLAUDECODEX",
	} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, Get(name), "Get(%q) must not resolve", name)
			assert.False(t, Exists(name), "Exists(%q) must be false", name)
			assert.False(t, EnforcesReadOnlyPlan(name), "EnforcesReadOnlyPlan(%q) must be false", name)
			assert.Nil(t, GetSettingsWriter(name, afero.NewMemMapFs()), "GetSettingsWriter(%q) must be nil", name)

			_, err := DecodeLLMConfig(name, map[string]interface{}{})
			assert.Error(t, err, "DecodeLLMConfig(%q) must refuse", name)

			_, err = SurfacesFor(name)
			assert.Error(t, err, "SurfacesFor(%q) must refuse", name)

			_, ok := VersionCommandFor(name)
			assert.False(t, ok, "VersionCommandFor(%q) must refuse", name)
		})
	}
}
