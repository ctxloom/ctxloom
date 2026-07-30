package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedAssets_CallerCannotCorruptThem is the isolation contract every
// accessor owes: each call hands back a private copy, so a caller that writes
// through the returned slice cannot change what the NEXT reader of that asset
// sees. The assets are the source of truth for every image build and probe in a
// run, and a corrupted one would surface as an unexplained build failure far
// from its cause.
func TestEmbeddedAssets_CallerCannotCorruptThem(t *testing.T) {
	for name, read := range map[string]func() []byte{
		"Base":         Base,
		"Entrypoint":   Entrypoint,
		"ProbeSeccomp": ProbeSeccomp,
	} {
		pristine := read()
		require.NotEmpty(t, pristine, "%s", name)

		mine := read()
		mine[0] = 'X'

		assert.Equal(t, pristine, read(), "%s: a caller's write must not reach the next reader", name)
	}
}
