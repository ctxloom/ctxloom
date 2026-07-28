package agent

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEphemeralPlacementDir(t *testing.T) {
	// A valid harp resolves to that harp's ephemeral directory.
	want, err := paths.HarpEphemeralDir("perky-same-chevy")
	require.NoError(t, err)
	p := ephemeralPlacement{harp: "perky-same-chevy"}
	assert.Equal(t, want, p.Dir())

	// An empty harp falls back to the OS temp dir.
	empty := ephemeralPlacement{harp: ""}
	assert.Equal(t, os.TempDir(), empty.Dir())
}
