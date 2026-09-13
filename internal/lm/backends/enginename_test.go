package backends

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// claudeCodeSpellings are every spelling of claude-code the repo-wide alias
// table accepts. Derived from the table (agent.EngineNameAliases) plus the
// case variants agent.CanonicalEngineName folds, so an alias added there is
// asserted here without an edit.
func claudeCodeSpellings(t *testing.T) []string {
	t.Helper()
	aliases := agent.EngineNameAliases("claude-code")
	require.NotEmpty(t, aliases, "the alias table declares no alias for claude-code — this test would be vacuous")
	spellings := append([]string{"claude-code", "CLAUDE-CODE", "Claude-Code"}, aliases...)
	for _, a := range aliases {
		spellings = append(spellings, strings.ToUpper(a))
	}
	return spellings
}

// TestRegistryLookups_ResolveEveryAcceptedSpelling pins the descriptor table to
// the shared engine-name vocabulary at each lookup a caller can enter by. The
// table is keyed by canonical names, so any lookup that indexed it raw answered
// "no such backend" for a spelling ltk and taskloom both resolve.
func TestRegistryLookups_ResolveEveryAcceptedSpelling(t *testing.T) {
	for _, spelling := range claudeCodeSpellings(t) {
		t.Run(spelling, func(t *testing.T) {
			b := Get(spelling)
			require.NotNil(t, b, "Get(%q) returned nil", spelling)
			assert.Equal(t, "claude-code", b.Name())

			assert.True(t, Exists(spelling), "Exists(%q)", spelling)
			assert.True(t, EnforcesReadOnlyPlan(spelling), "EnforcesReadOnlyPlan(%q)", spelling)

			cfg, err := DecodeLLMConfig(spelling, map[string]interface{}{})
			require.NoError(t, err, "DecodeLLMConfig(%q)", spelling)
			assert.Equal(t, "claude-code", cfg.BackendType())

			assert.NotNil(t, GetSettingsWriter(spelling, afero.NewMemMapFs()), "GetSettingsWriter(%q)", spelling)

			set, err := SurfacesFor(spelling)
			require.NoError(t, err, "SurfacesFor(%q)", spelling)
			assert.NotNil(t, set)

			canonicalCmd, ok := VersionCommandFor("claude-code")
			require.True(t, ok)
			cmd, ok := VersionCommandFor(spelling)
			require.True(t, ok, "VersionCommandFor(%q)", spelling)
			assert.Equal(t, canonicalCmd.Args, cmd.Args, "VersionCommandFor(%q) disagrees with the canonical name", spelling)
		})
	}
}

// TestRegistryLookups_StillRefuseAnUnknownName is the other half: resolving
// aliases must not have widened into rounding a typo to a real backend. Every
// lookup must answer for an unknown name exactly what it answered before —
// nothing, not a default.
func TestRegistryLookups_StillRefuseAnUnknownName(t *testing.T) {
	// "antigravity" is a RETIRED engine name, kept deliberately: a stale config
	// naming a backend this build has removed must be refused, not silently
	// accepted. It is a rejection case, not a live roster member.
	for _, name := range []string{"totally-bogus", "clau", "claude-", "", "antigravity", "CLAUDECODEX"} {
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

// TestRegister_NameClaimedAsAnotherEnginesAliasIsAnError pins the key side of
// the table. A descriptor registered under a spelling another engine already
// declares as its alias lands where no lookup can reach it, and the backend
// reads as having no capabilities at all rather than as misregistered.
func TestRegister_NameClaimedAsAnotherEnginesAliasIsAnError(t *testing.T) {
	err := Register(enginefixture.Descriptor("claude"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another engine's alias (resolves to claude-code)")
}

// TestRegister_AliasOwnedByAnotherEngineIsAnError pins the other side: an
// engine cannot claim a spelling that already resolves to a different
// engine, and the refused batch installs nothing — the table is populated
// from descriptors, so the conflict is found before either side lands.
func TestRegister_AliasOwnedByAnotherEngineIsAnError(t *testing.T) {
	d := enginefixture.Descriptor("fixture-claimant")
	d.Aliases = []string{"claude"}
	err := Register(d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `alias "claude" already resolves to "claude-code"`)
	assert.False(t, Exists("fixture-claimant"), "a refused batch installs nothing")
	assert.Equal(t, "claude-code", agent.CanonicalEngineName("claude"))
}

// TestRegister_PopulatesTheAliasTable: the table is what the descriptors
// declare — an engine's spellings resolve because it registered, and to it.
func TestRegister_PopulatesTheAliasTable(t *testing.T) {
	d := enginefixture.Descriptor("fixture-spelled")
	d.Aliases = []string{"fixture-sp", "fsp"}
	require.NoError(t, Register(d))
	t.Cleanup(func() { UnregisterForTesting("fixture-spelled") })
	assert.Equal(t, "fixture-spelled", agent.CanonicalEngineName("FSP"))
	assert.Equal(t, []string{"fixture-sp", "fsp"}, agent.EngineNameAliases("fixture-spelled"))
	assert.True(t, Exists("fixture-sp"), "the registry resolves through the spellings it populated")
}
