package confpatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// An existing target whose bytes are an EMPTY flow container must take a
// write and give it back byte-exact. proveReversal checks the stored inverse
// against the before-image, so a hew whose insert into an empty container is
// not injective refuses the very first write to a user's "{}".
func TestAnEmptyContainerTargetTakesAWriteAndGivesItBack(t *testing.T) {
	for _, base := range []string{"{}", "{}\n", "{\n}\n", "{  }", `{"a": 1}`, `{"mcpServers": {}}`} {
		t.Run(base, func(t *testing.T) {
			s, fs := newStore(t)
			const target = "/proj/.mcp.json"
			testsupport.WriteFileString(t, fs, target, base, 0o644)

			_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
			require.NoError(t, err, "a write to %q must be accepted", base)
			require.Contains(t, mustRead(t, fs, target), `"ctxloom"`)

			_, err = s.Apply(fs, target, recordNothing())
			require.NoError(t, err)
			assert.Equal(t, base, mustRead(t, fs, target))
		})
	}
}
