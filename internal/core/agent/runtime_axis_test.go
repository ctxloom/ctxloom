package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseRuntimeAxis is the runtime vocabulary's own contract, the twin of
// isolation.ParseWorkspaceAxis's: the three members round-trip, empty passes
// through as "this level said nothing", and everything else is refused naming
// the legal set.
//
// The EMPTY case is load-bearing and not a formality. An unset axis is not a
// typo — an agent that declares no runtime, and a project with no `runtime:`
// default, both resolve to host downstream — so the parser must hand silence
// back unchanged. Turning unset into an error would refuse every
// default-shaped config in existence; turning it into a literal "host" would
// change what a caller serializing the resolved axis writes.
//
// The refusal is not cosmetic: the loop below shows what an unrecognized
// spelling asserted PAST this parser would have meant. It reads as
// not-a-container, which is the host — so a config asking to be inside a
// container boundary would have run outside one, with nothing said. That is
// why this axis refuses where the advisory workspace axis may warn.
func TestParseRuntimeAxis(t *testing.T) {
	for _, member := range RuntimeNames() {
		got, err := ParseRuntimeAxis(member)
		require.NoError(t, err, "%q is a declared member", member)
		assert.Equal(t, RuntimeAxis(member), got)
	}
	require.Equal(t, []string{"host", "container-rootless", "container-rootful"}, RuntimeNames())

	got, err := ParseRuntimeAxis("")
	require.NoError(t, err, "unset is not an error")
	assert.Equal(t, RuntimeAxis(""), got, "unset passes through as itself, not as a literal host")
	assert.False(t, IsContainerRuntimeAxis(got),
		"unset asks the same question as host and gets the same answer — that equivalence is what lets it pass through")

	for _, bad := range []string{"contianer-rootless", "container", "container-rootless ", "Host", "vm", "none"} {
		got, err := ParseRuntimeAxis(bad)
		require.Error(t, err, "%q is not a member", bad)
		assert.Equal(t, RuntimeAxis(""), got)
		assert.Contains(t, err.Error(), bad)
		assert.Contains(t, err.Error(), "host|container-rootless|container-rootful")
		assert.False(t, IsContainerRuntimeAxis(RuntimeAxis(bad)),
			"an unparsed spelling really would have read as no container boundary at all")
	}
}
