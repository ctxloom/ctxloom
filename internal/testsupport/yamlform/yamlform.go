// Package yamlform asserts that bytes a writer saved are already in the form
// the upgrade write-back encodes, so persisting an upgrade (--write-upgrades)
// and a later ordinary save of the same content never rewrite each other.
package yamlform

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// RequireWriteBackForm fails t unless saved, re-encoded the way
// schemaver.WriteBack persists an upgraded document, comes back byte for byte.
// saved must exercise nesting (a mapping or sequence under a key): a flat
// document reads the same at every indentation and proves nothing.
func RequireWriteBackForm(t testing.TB, saved []byte) {
	t.Helper()
	doc, err := upgrade.DecodeSingle(saved)
	require.NoError(t, err, "saved bytes must be one YAML document:\n%s", saved)
	again, err := upgrade.Encode(&doc)
	require.NoError(t, err)
	require.Equal(t, string(again), string(saved),
		"a save must write the write-back encoding, or an upgraded file and a saved one rewrite each other")
}
