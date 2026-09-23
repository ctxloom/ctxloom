package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
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

// TestTokenAuthFor_OnlyTheRegisteredNameResolves: the token-auth table gates
// both the stored-token export and the unauthenticated-home refusal, so the
// registered name must hit, and every other spelling — unknown, alias-shaped
// or case-variant — must miss rather than be rounded to a real engine.
func TestTokenAuthFor_OnlyTheRegisteredNameResolves(t *testing.T) {
	_, ok := TokenAuthFor(claude.EngineName)
	assert.True(t, ok, "the registered name resolves")
	for _, spelling := range append(nonRegisteredSpellings(), unknownEngineName) {
		_, ok := TokenAuthFor(spelling)
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
