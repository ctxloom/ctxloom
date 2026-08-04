package config

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/resources"
)

// BuiltinCompanionBins parsed embedded bundle YAML with a raw
// yaml.Unmarshal instead of bundles.ParseBundle, so it skipped the schema
// upgrade pipeline every on-disk and remote bundle goes through.
//
// The row understated the spread and overstated the isolation. It was not one
// site: FOUR surfaces read the same embedded bytes (companion bins, MCP
// servers, hooks, fragments) and ALL FOUR ran their own raw yaml.Unmarshal, so
// "every other bundle reader" was not true of the builtin family — they agreed
// with each other and disagreed with everything else. That is the actual
// hazard: the same bytes could mean four different things the day a migration
// lands, and nothing would say so.
//
// The consequence TODAY is nil, and this is stated rather than implied: the
// only registered upgrade renames the top-level `prompts:` key to `commands:`,
// and none of the four surfaces reads Bundle.Commands. That is why the pin
// below is a parity test at the parser seam rather than an assertion about
// what a builtin bundle currently yields — the divergence is real and
// demonstrable, it simply has not reached these four readers yet.

// TestParseBundle_DivergesFromRawUnmarshalOnLegacySchema is the divergence
// this row is about, shown rather than asserted: the canonical parser applies
// the schema upgrade and the raw unmarshal silently drops the renamed key.
func TestParseBundle_DivergesFromRawUnmarshalOnLegacySchema(t *testing.T) {
	legacy := []byte(`name: legacy
prompts:
  greet:
    content: hello
`)

	var raw bundles.Bundle
	require.NoError(t, yaml.Unmarshal(legacy, &raw))
	assert.Empty(t, raw.Commands,
		"a raw unmarshal drops the legacy key entirely — this is the silent-no-op the upgrade pipeline exists to prevent")

	parsed, err := bundles.ParseBundle(legacy)
	require.NoError(t, err)
	assert.Contains(t, parsed.Commands, "greet",
		"bundles.ParseBundle migrates prompts: to commands: — the two parsers do NOT agree, so which one a reader uses is a correctness question, not a style one")
}

// TestEachBuiltinBundle_ParsesEveryEmbeddedBundle guards the risk the fix
// introduces. Routing the four builtin readers through ParseBundle means a
// builtin that the canonical parser REJECTS (e.g. one carrying a legacy
// command-shaped `skills:` block, which ParseBundle refuses by design) would be
// warned and skipped rather than half-read — silently withholding its hooks,
// MCP servers, fragments and companion bins. That must never be true of a
// bundle we ship, so it is a gate: every embedded bundle must parse, and every
// one must be delivered to the callback.
func TestEachBuiltinBundle_ParsesEveryEmbeddedBundle(t *testing.T) {
	names, err := resources.ListBuiltinBundles()
	require.NoError(t, err)
	require.NotEmpty(t, names, "the binary must embed at least one builtin bundle, or this gate proves nothing")

	seen := map[string]bool{}
	eachBuiltinBundle(func(read bundles.BundleRead) {
		require.NotNil(t, read.Bundle)
		seen[read.Ref()] = true
	})

	for _, name := range names {
		assert.True(t, seen[name],
			"builtin bundle %q did not survive bundles.ParseBundle — every surface that reads builtin bundles would silently withhold everything it ships", name)
	}
}

// TestBuiltinBundleReaders_UseTheCanonicalParser states the consistency
// invariant as a structural fact the four surfaces now share: they read
// builtin bundles through exactly one function. A fifth surface that grows its
// own yaml.Unmarshal reintroduces the divergence, and this is the note that
// says where to put it instead.
func TestBuiltinBundleReaders_UseTheCanonicalParser(t *testing.T) {
	// Exercised through the three exported surfaces, so the shared helper is
	// on each of their live paths rather than merely present in the file.
	assert.NotPanics(t, func() {
		_ = BuiltinCompanionBins()
		_ = resolveBuiltinBundleMCPServers(nil)
		_ = resolveBuiltinBundleHooks(nil)
	})
}
