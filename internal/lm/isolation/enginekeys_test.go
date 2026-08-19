package isolation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// unknownEngineName is a spelling no registry knows and the alias table does
// not rewrite. It stands for the legitimate miss every table must keep
// answering: canonicalizing must not round an unknown name to a real engine.
const unknownEngineName = "definitely-not-an-engine"

// aliasPairs derives the (canonical, alias) corpus from agent.EngineNameAliases
// for the given roster, rather than listing spellings. A hand-listed corpus
// silently stops covering a newly declared alias — this one gains it the moment
// the alias table does.
func aliasPairs(t *testing.T, roster []string) [][2]string {
	t.Helper()
	require.NotEmpty(t, roster, "roster is empty: the corpus would be vacuous")
	var pairs [][2]string
	for _, canonical := range roster {
		for _, alias := range agent.EngineNameAliases(canonical) {
			pairs = append(pairs, [2]string{canonical, alias})
		}
	}
	require.NotEmpty(t, pairs, "no declared alias covers any member of %v: the corpus would be vacuous", roster)
	return pairs
}

// upperSpelling returns name in upper case. Canonicalization has two halves —
// lowercasing and alias resolution — and a roster whose members declare no
// alias (the exemption list, whose only entry is "mock") can only be covered
// through this one. Derived from the roster's own entries, never listed.
func upperSpelling(t *testing.T, name string) string {
	t.Helper()
	upper := strings.ToUpper(name)
	require.NotEqual(t, name, upper, "fixture: %q must have a distinct upper-case spelling", name)
	return upper
}

// TestCredentialSeedSpecFor_ResolvesDeclaredAliases is the credential-seed half
// of the silent no-seed hazard: a lookup keyed by an alias must reach the same
// descriptor the canonical name does, or an isolated run seeds no credentials
// and says nothing.
func TestCredentialSeedSpecFor_ResolvesDeclaredAliases(t *testing.T) {
	for _, pair := range aliasPairs(t, CredentialSeedEngineNames()) {
		canonical, alias := pair[0], pair[1]

		want, ok := credentialSeedSpecFor(canonical)
		require.True(t, ok, "fixture: %q must be registered in credentialSeedSpecs", canonical)

		got, ok := credentialSeedSpecFor(alias)
		require.True(t, ok, "alias %q of %q must resolve to a credential-seed spec", alias, canonical)
		assert.Equal(t, want.engine, got.engine, "alias %q must reach %q's own spec", alias, canonical)
		assert.Equal(t, want.destSubdir, got.destSubdir, "alias %q must reach %q's own spec", alias, canonical)
		assert.Equal(t, want.HomeVars, got.HomeVars, "alias %q must reach %q's own home vars", alias, canonical)
	}
}

// TestCredentialSeedSpecFor_ResolvesCaseVariants covers the other half of
// canonicalization: an engine name differing only in case is the same engine.
func TestCredentialSeedSpecFor_ResolvesCaseVariants(t *testing.T) {
	roster := CredentialSeedEngineNames()
	require.NotEmpty(t, roster, "fixture: credentialSeedSpecs must not be empty")
	for _, canonical := range roster {
		upper := upperSpelling(t, canonical)
		_, ok := credentialSeedSpecFor(upper)
		assert.True(t, ok, "%q must resolve to %q's spec", upper, canonical)
	}
}

// TestCredentialSeedSpecFor_UnknownEngineStillMisses proves the negative: a
// name nobody registered must keep missing. Trading a silent miss for a silent
// default would be worse than the bug being fixed.
func TestCredentialSeedSpecFor_UnknownEngineStillMisses(t *testing.T) {
	require.NotEmpty(t, credentialSeedSpecs, "fixture: credentialSeedSpecs must not be empty")

	_, ok := credentialSeedSpecFor(unknownEngineName)
	assert.False(t, ok, "an unregistered engine must not resolve to any spec")

	assert.Nil(t, CredentialSeedHomeVars(unknownEngineName), "an unregistered engine exports no scoped home var")
	subdir, ok := CredentialSeedDestSubdir(unknownEngineName)
	assert.False(t, ok, "an unregistered engine has no destination subdir")
	assert.Empty(t, subdir)
	assert.Nil(t, CredentialSeedSourceFiles(unknownEngineName), "an unregistered engine has no seed files")
	assert.Nil(t, AmbientSet(unknownEngineName), "an unregistered engine has no ambient allow-list")

	// No fuzzy rounding: a near-miss of a real engine name is still a miss.
	_, ok = credentialSeedSpecFor("claude-cod")
	assert.False(t, ok, "a prefix of a real engine name must not resolve to it")
}

// TestBackendHasNoGlobalState_ResolvesAliasesAndKeepsUnknownFalse pins the
// exemption list: an aliased spelling must keep the exemption, and an engine
// that is not on the list must not acquire one.
func TestBackendHasNoGlobalState_ResolvesAliasesAndKeepsUnknownFalse(t *testing.T) {
	require.NotEmpty(t, backendsWithNoGlobalState, "fixture: the exemption list must not be empty")
	for name := range backendsWithNoGlobalState {
		require.True(t, backendHasNoGlobalState(name), "fixture: %q must be exempt", name)
		for _, alias := range agent.EngineNameAliases(name) {
			assert.True(t, backendHasNoGlobalState(alias), "alias %q of %q must keep the exemption", alias, name)
		}
		assert.True(t, backendHasNoGlobalState(upperSpelling(t, name)),
			"a case variant of %q must keep the exemption", name)
	}
	assert.False(t, backendHasNoGlobalState(unknownEngineName), "an unlisted backend must not be exempt")
	assert.False(t, backendHasNoGlobalState(""), "the no-context construction is handled by its own guard, not the exemption list")
}

// TestEngineContainerSpecFor_ResolvesDeclaredAliases is the container half of
// the same hazard: an aliased engine reaching the fail-closed default arm
// cannot authenticate at all, and config validation would refuse the binding.
func TestEngineContainerSpecFor_ResolvesDeclaredAliases(t *testing.T) {
	for _, pair := range aliasPairs(t, ContainerAuthEngines()) {
		canonical, alias := pair[0], pair[1]

		require.True(t, HasContainerAuth(canonical), "fixture: %q must have container auth", canonical)
		assert.True(t, HasContainerAuth(alias), "alias %q of %q must have container auth", alias, canonical)
		assert.Equal(t, ContainerOverlayDirsFor(canonical), ContainerOverlayDirsFor(alias),
			"alias %q must reach %q's own overlay dirs", alias, canonical)
		assert.Equal(t, ContainerTranscriptStoreRelFor(canonical), ContainerTranscriptStoreRelFor(alias),
			"alias %q must reach %q's own transcript store", alias, canonical)
	}
}

// TestEngineContainerSpecFor_UnknownEngineStillFailsClosed proves the container
// negative: an unmapped engine must still land on the fail-closed default,
// never inherit another engine's credentials.
func TestEngineContainerSpecFor_UnknownEngineStillFailsClosed(t *testing.T) {
	require.NotEmpty(t, ContainerAuthEngines(), "fixture: the container-auth roster must not be empty")

	assert.False(t, HasContainerAuth(unknownEngineName), "an unmapped engine must reach the fail-closed default")
	assert.False(t, HasContainerAuth(""), "an empty engine name must reach the fail-closed default")
	assert.False(t, HasContainerAuth("acp"), "the generic acp backend has no vetted container auth")

	spec := engineContainerSpecFor(unknownEngineName)
	assert.Equal(t, noContainerAuthHint, spec.authHint, "the default arm's marker hint identifies it")
	assert.Equal(t, defaultOverlayDirs, spec.overlayDirs, "an unmapped engine keeps the default overlay dirs")
}

// TestResolveEngines_AcceptsDeclaredAliases pins the composed-image engine set:
// a configured alias names a real engine, so it must be kept, not dropped as
// unknown.
func TestResolveEngines_AcceptsDeclaredAliases(t *testing.T) {
	for _, pair := range aliasPairs(t, composableEngines()) {
		canonical, alias := pair[0], pair[1]
		assert.Equal(t, []string{canonical}, resolveEngines([]string{alias}),
			"configured alias %q must resolve to %q", alias, canonical)
	}
	assert.Nil(t, resolveEngines([]string{unknownEngineName}), "an unknown configured engine is still dropped")
}

// TestRegisterInstanceConfigWriter_ResolvesAliasesAndRefusesNonCanonicalKeys
// covers the runtime-populated writer table on both sides: a key the alias
// table would rewrite is refused at registration, and an aliased lookup reaches
// the registered writer.
func TestRegisterInstanceConfigWriter_ResolvesAliasesAndRefusesNonCanonicalKeys(t *testing.T) {
	pairs := aliasPairs(t, CredentialSeedEngineNames())
	canonical, alias := pairs[0][0], pairs[0][1]

	writer := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, canonical, writer)

	require.NotNil(t, instanceConfigWriterFor(canonical), "fixture: the writer must be registered under %q", canonical)
	assert.NotNil(t, instanceConfigWriterFor(alias), "alias %q must reach %q's registered writer", alias, canonical)
	assert.Nil(t, instanceConfigWriterFor(unknownEngineName), "an unregistered engine has no writer")

	assert.Panics(t, func() { RegisterInstanceConfigWriter(alias, writer) },
		"registering under alias %q would install the writer where no lookup can reach it", alias)
}

// TestRegisterCredentialProjector_ResolvesAliasesAndRefusesNonCanonicalKeys is
// the projector twin of the writer test above.
func TestRegisterCredentialProjector_ResolvesAliasesAndRefusesNonCanonicalKeys(t *testing.T) {
	pairs := aliasPairs(t, CredentialSeedEngineNames())
	canonical, alias := pairs[0][0], pairs[0][1]

	projector := &fakeCredentialProjector{}
	withCredentialProjector(t, canonical, projector)

	require.NotNil(t, credentialProjectorFor(canonical), "fixture: the projector must be registered under %q", canonical)
	assert.NotNil(t, credentialProjectorFor(alias), "alias %q must reach %q's registered projector", alias, canonical)
	assert.Nil(t, credentialProjectorFor(unknownEngineName), "an unregistered engine has no projector")

	assert.Panics(t, func() { RegisterCredentialProjector(alias, projector) },
		"registering under alias %q would install the projector where no lookup can reach it", alias)
}

// TestEngineKeyedTables_HoldCanonicalKeys is the observable form of
// enginekeys.go's init assertion: a table entry keyed under a spelling the
// alias table rewrites is unreachable by any lookup.
func TestEngineKeyedTables_HoldCanonicalKeys(t *testing.T) {
	rosters := map[string][]string{
		"credentialSeedSpecs":       CredentialSeedEngineNames(),
		"ContainerAuthEngines":      ContainerAuthEngines(),
		"composableEngines":         composableEngines(),
		"backendsWithNoGlobalState": noGlobalStateNames(),
	}
	for table, names := range rosters {
		require.NotEmpty(t, names, "fixture: roster %s must not be empty", table)
		for _, name := range names {
			assert.Equal(t, agent.CanonicalEngineName(name), name, "%s key %q must be canonical", table, name)
		}
	}
}

func noGlobalStateNames() []string {
	names := make([]string, 0, len(backendsWithNoGlobalState))
	for name := range backendsWithNoGlobalState {
		names = append(names, name)
	}
	return names
}
