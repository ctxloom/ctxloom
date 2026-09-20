package composite

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cbroglie/mustache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/resources"
)

// packageDir is this package's directory, captured before the test
// binary's sandbox moves the working directory (-trimpath strips
// runtime.Caller's path, so the directory is the only anchor).
var packageDir, _ = os.Getwd()

// TestShippedFragments_NoUnescapedForeignMustache is a guard against the
// corruption class characterized by
// TestSubstituteVariables_LiteralJustSyntaxIsCorrupted recurring in a real,
// shipped fragment: it scans every fragment this repo actually ships and
// fails if any fragment's content contains a `{{...}}`-shaped tag, outside a
// Set Delimiter escape block, that does not resolve to a known ctxloom
// variable.
//
// Coverage — this checks exactly, and only, the fragment content this repo
// compiles in or ships alongside its own binaries:
//   - every embedded builtin bundle (resources/builtin_bundles/*.yaml, via
//     resources.ListBuiltinBundles/GetBuiltinBundle)
//   - the two standalone companion loadouts (cmd/taskloom/loadout.yaml,
//     cmd/ltk/loadout.yaml), which ship the same bundle document shape
//
// It deliberately does NOT cover, and cannot cover from inside this repo:
//   - remote-pulled bundle content (ctxloom-default or any other remote a
//     project's profile references) — those bytes live outside this repo
//     and are not available at test time
//   - a consuming project's own .ctxloom/ fragments and profiles
//   - `commands:` content, which never passes through substituteVariables
//     (it uses the separate, positional-arg-rewrite export mechanism
//     documented in docs/guides/templating.md's "Commands are different"
//     note)
//
// shippedFragmentKnownVariables is the allowlist of ctxloom variable names a
// shipped fragment is genuinely allowed to reference. It is empty today: no
// fragment this repo ships declares or depends on a profile variable. If a
// future shipped fragment legitimately needs one, add its name here
// deliberately — do not delete or loosen this check to make it pass.
func TestShippedFragments_NoUnescapedForeignMustache(t *testing.T) {
	shippedFragmentKnownVariables := map[string]bool{}

	type fragmentSource struct {
		label   string
		content string
	}
	var sources []fragmentSource

	collect := func(label string, raw []byte) {
		bundle, err := bundles.ParseBundle(raw)
		require.NoError(t, err, "%s: must parse as a bundle document", label)
		for name, frag := range bundle.Fragments {
			sources = append(sources, fragmentSource{
				label:   label + "#fragments/" + name,
				content: frag.Content,
			})
		}
	}

	builtinNames, err := resources.ListBuiltinBundles()
	require.NoError(t, err)
	require.NotEmpty(t, builtinNames, "no builtin bundles found — this guard would silently check nothing")
	for _, name := range builtinNames {
		raw, err := resources.GetBuiltinBundle(name)
		require.NoError(t, err)
		collect("builtin_bundles/"+name, raw)
	}

	for _, rel := range []string{
		filepath.Join("..", "..", "..", "cmd", "taskloom", "loadout.yaml"),
		filepath.Join("..", "..", "..", "cmd", "ltk", "loadout.yaml"),
	} {
		path := filepath.Join(packageDir, rel)
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "companion loadout must exist: %s", path)
		collect(rel, raw)
	}

	require.NotEmpty(t, sources, "no fragment sources discovered — this guard would silently check nothing")

	knownVars := make(map[string]string, len(shippedFragmentKnownVariables))
	for name := range shippedFragmentKnownVariables {
		knownVars[name] = "x"
	}

	for _, src := range sources {
		src := src
		t.Run(src.label, func(t *testing.T) {
			tmpl, err := mustache.ParseString(src.content)
			require.NoError(t, err, "fragment content failed to parse as a mustache template")

			var undefined []string
			checkTags(tmpl.Tags(), knownVars, collections.NewSet[string](), func(msg string) {
				undefined = append(undefined, msg)
			})

			assert.Empty(t, undefined,
				"fragment %q references an un-escaped {{...}} tag that is not a known ctxloom "+
					"variable — if this is literal prose for another {{}}-flavored syntax, wrap it in "+
					"Mustache's Set Delimiter escape ({{=<% %>=}} ... <%={{ }}=%>); if it is a genuine "+
					"new ctxloom variable this fragment now depends on, add it to "+
					"shippedFragmentKnownVariables deliberately", src.label)
		})
	}
}
