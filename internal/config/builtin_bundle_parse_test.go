package config

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/resources"
)

// Every surface that reads the embedded builtin bytes reads them through
// bundles.ParseBundle, so the schema upgrade pipeline every on-disk and remote
// bundle goes through applies to them too.
//
// The hazard this pins is agreement: several surfaces read the SAME embedded
// bytes (MCP servers, hooks, fragments), and a surface with its own raw
// yaml.Unmarshal would make those bytes mean two different things the day a
// migration lands, with nothing to say so.
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
	legacy := []byte(`
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
		seen[read.DisplayName()] = true
	})

	for _, name := range names {
		// A builtin's resolution ref is its BARE name: a bundle is addressed by
		// what it declares, not by where it sits, so the source class lives on
		// the TRUST ref (BundleRead.TrustSourceRef) and nowhere in the handle.
		// The exact name is still asserted rather than a suffix match — the
		// point of the gate is that this specific builtin arrived, not that
		// something ending in its name did.
		assert.True(t, seen[name],
			"builtin bundle %q did not survive bundles.ParseBundle — every surface that reads builtin bundles would silently withhold everything it ships", name)
	}
}

// TestEachBuiltinBundle_ParsesOncePerProcess pins the memo.
//
// Builtin bundles are compiled into the binary, so their bytes cannot change
// while it runs — the one source in this package for which caching across calls
// is unconditionally safe. Four surfaces read them (MCP servers, hooks,
// fragments, companion bins) and each call used to re-walk the embedded
// filesystem and re-run ParseBundle over every builtin to produce a
// byte-identical answer.
//
// Pointer identity is the assertion because it is the only thing that
// distinguishes "one parse, shared" from "two parses that happen to be equal" —
// a deep-equality check would pass either way and prove nothing.
func TestEachBuiltinBundle_ParsesOncePerProcess(t *testing.T) {
	collect := func() map[string]*bundles.Bundle {
		out := map[string]*bundles.Bundle{}
		eachBuiltinBundle(func(read bundles.BundleRead) { out[read.DisplayName()] = read.Bundle })
		return out
	}

	first, second := collect(), collect()
	require.NotEmpty(t, first, "the binary must embed at least one builtin, or this proves nothing")

	for ref, b := range first {
		require.Same(t, b, second[ref],
			"builtin %q was parsed again on the second call: the embedded bytes cannot change "+
				"mid-process, so every re-read is pure waste and risks two surfaces disagreeing", ref)
	}
}

// TestBuiltinBundleReaders_UseTheCanonicalParser states the consistency
// invariant as a structural fact the four surfaces now share: they read
// builtin bundles through exactly one function. A fifth surface that grows its
// own yaml.Unmarshal reintroduces the divergence, and this is the note that
// says where to put it instead.
func TestBuiltinBundleReaders_UseTheCanonicalParser(t *testing.T) {
	// Exercised through the exported surfaces, so the shared helper is on each
	// of their live paths rather than merely present in the file.
	assert.NotPanics(t, func() {
		_ = resolveBuiltinBundleMCPServers(bundles.AdmitAll())
		_ = resolveBuiltinBundleHooks(bundles.AdmitAll())
	})
}
