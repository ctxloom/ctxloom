package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Undeclared is today's behaviour: the engine inherits every variable.
func TestHostEnv_UndeclaredInheritsEverything(t *testing.T) {
	assert.True(t, HostEnv{}.Inherits("UNRELATED_SECRET"))
}

// Curated keeps the base (exact names and prefixes) and the passthrough, and
// nothing else — an exact name is not a prefix.
func TestHostEnv_CuratedKeepsOnlyTheBaseAndThePassthrough(t *testing.T) {
	h := HostEnv{Curated: true, Passthrough: []string{"GITHUB_TOKEN"}}
	for _, k := range []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "COLORTERM", "LANG", "TMPDIR", "LC_ALL", "XDG_RUNTIME_DIR", "CTXLOOM_HARP", "GITHUB_TOKEN"} {
		assert.True(t, h.Inherits(k), "%s is kept", k)
	}
	for _, k := range []string{"UNRELATED_SECRET", "AWS_SECRET_ACCESS_KEY", "PATHX", "LC", "XDG", "GITHUB_TOKENX"} {
		assert.False(t, h.Inherits(k), "%s is dropped", k)
	}
}

// A passthrough with no opt-in would be silently ignored; it is refused.
func TestHostEnv_PassthroughWithoutCuratedIsRefused(t *testing.T) {
	require.ErrorIs(t, HostEnv{Passthrough: []string{"X"}}.Validate(), ErrHostEnvPassthroughUncurated)
	require.NoError(t, HostEnv{Curated: true, Passthrough: []string{"X"}}.Validate())
	require.NoError(t, HostEnv{}.Validate())
}
