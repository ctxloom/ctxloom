package engineversion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeadingToken(t *testing.T) {
	v := LeadingToken("1.2.3")
	assert.Equal(t, []string{"--version"}, v.Args)
	assert.Equal(t, "1.2.3", v.Floor)
	got, err := v.Parse("\n1.18.35\n")
	require.NoError(t, err)
	assert.Equal(t, "1.18.35", got)
	got, err = v.Parse("2.1.286 (Claude Code)")
	require.NoError(t, err)
	assert.Equal(t, "2.1.286", got)
	_, err = v.Parse("opencode v1")
	require.Error(t, err, "the leading token must be the version")
}
