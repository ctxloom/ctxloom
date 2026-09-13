package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
)

// unknownEngineName is a spelling no registry knows. It stands for the
// legitimate miss every engine-keyed table must keep answering.
const unknownEngineName = "definitely-not-an-engine"

// nonRegisteredSpellings are spellings of a REAL engine that are not its
// registered name. An engine has exactly one name; every table here is keyed
// on it and looked up by exact match, so each of these is a miss — there is
// no alias, case or prefix resolution to round them to the engine.
func nonRegisteredSpellings() []string {
	return []string{"claude", "claudecode", "Claude-Code", "CLAUDE-CODE", "claude-cod"}
}

// TestCredentialSeedSpecFor_OnlyTheRegisteredNameResolves: the seed table
// gates credential seeding, and a miss is silent (nothing is seeded). So the
// registered name must hit, and every other spelling — unknown, alias-shaped
// or case-variant — must miss rather than be rounded to a real engine.
func TestCredentialSeedSpecFor_OnlyTheRegisteredNameResolves(t *testing.T) {
	require.Contains(t, CredentialSeedEngineNames(), claude.EngineName, "fixture: claude must be registered at this seam")

	_, ok := credentialSeedDeclared(claude.EngineName)
	assert.True(t, ok, "the registered name resolves")

	_, ok = credentialSeedDeclared(unknownEngineName)
	assert.False(t, ok, "an unregistered engine must not resolve to any declaration")
	assert.Nil(t, AmbientSet(unknownEngineName), "an unregistered engine has no ambient allow-list")
	for _, spelling := range nonRegisteredSpellings() {
		_, ok := credentialSeedDeclared(spelling)
		assert.False(t, ok, "%q is not the registered name and must miss", spelling)
	}
}

// TestEngineContainerSpecFor_OnlyTheRegisteredNameResolves is the container
// half: an unmapped engine must land on the fail-closed default, never
// inherit another engine's credentials — and a non-registered spelling of a
// real engine is unmapped.
func TestEngineContainerSpecFor_OnlyTheRegisteredNameResolves(t *testing.T) {
	require.NotEmpty(t, ContainerAuthEngines(), "fixture: the container-auth roster must not be empty")
	assert.True(t, HasContainerAuth(claude.EngineName), "the registered name resolves")

	assert.False(t, HasContainerAuth(unknownEngineName), "an unmapped engine must reach the fail-closed default")
	assert.False(t, HasContainerAuth(""), "an empty engine name must reach the fail-closed default")
	assert.False(t, HasContainerAuth("acp"), "the generic acp backend has no vetted container auth")
	for _, spelling := range nonRegisteredSpellings() {
		assert.False(t, HasContainerAuth(spelling), "%q is not the registered name and must miss", spelling)
	}

	spec := engineContainerSpecFor(unknownEngineName)
	assert.Equal(t, noContainerAuthHint, spec.authHint, "the default arm's marker hint identifies it")
	assert.Equal(t, []string{ctxloomCacheOverlayDir}, spec.overlayDirs, "an unmapped engine shadows only ctxloom's own cache dir")
}

// TestInstanceConfigWriterFor_OnlyTheRegisteredNameResolves covers the two
// runtime-populated tables: a writer or projector registered under a name is
// reachable under exactly that name.
func TestInstanceConfigWriterFor_OnlyTheRegisteredNameResolves(t *testing.T) {
	assert.NotNil(t, credentialProjectorFor(claude.EngineName), "fixture: claude's projector is registered by TestMain")
	for _, spelling := range append(nonRegisteredSpellings(), unknownEngineName) {
		assert.Nil(t, instanceConfigWriterFor(spelling), "instanceConfigWriterFor(%q)", spelling)
		assert.Nil(t, credentialProjectorFor(spelling), "credentialProjectorFor(%q)", spelling)
	}
}
