package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPathSeam_TargetsNeverReadSource: with BOTH rules non-identity, and
// disagreeing about the same path, the mount's target is the target rule's
// answer alone and its Host stays the path this process sees; the source rule
// shows up only in the rendered --mount source.
func TestPathSeam_TargetsNeverReadSource(t *testing.T) {
	const p = "/__w/proj"
	s := pathSeam{
		target: prefixMapper{prefix: "/ctr"},
		source: selfMountSource{mounts: []selfMount{{source: "/daemon/work", destination: "/__w"}}},
	}
	target, err := s.targetFor(p)
	require.NoError(t, err)
	source, err := s.sourceFor(p)
	require.NoError(t, err)
	require.Equal(t, "/ctr/__w/proj", target)
	require.Equal(t, "/daemon/work/proj", source)

	m, err := s.expose(p, true)
	require.NoError(t, err)
	assert.Equal(t, mount{Host: p, Container: target, ReadOnly: true}, m)

	args, err := renderRunSpec(RunSpec{Image: "img", Mounts: []mount{m}}, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"--mount", "type=bind,source=" + source + ",target=" + target + ",readonly", "img"}, args)
}
