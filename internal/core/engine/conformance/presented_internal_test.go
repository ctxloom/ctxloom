package conformance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestPresentedValues_CoversTheEnvChannel: the container-path check reads
// every value a presentation announces, the environment as well as argv, so
// an engine announcing its config dir through an env var cannot hand the
// engine a HOST path unseen. Env values come in key order.
func TestPresentedValues_CoversTheEnvChannel(t *testing.T) {
	got := presentedValues(present.Presentation{
		Args: []string{"--settings", "/e/home/settings.json"},
		Env:  map[string]string{"Z_DIR": "/e/home/z", "A_DIR": "/e/project/a"},
	})
	require.Equal(t, []announced{
		{channel: "argv", value: "--settings"},
		{channel: "argv", value: "/e/home/settings.json"},
		{channel: "env A_DIR", value: "/e/project/a"},
		{channel: "env Z_DIR", value: "/e/home/z"},
	}, got)
}
