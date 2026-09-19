package composite

import (
	"testing"

	"github.com/cbroglie/mustache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// TestMustacheTagClassification pins the behaviour before the local
// TagType re-declaration is removed. Three untyped ints mirrored
// cbroglie/mustache's exported TagType enum by value, which is connascence of
// value with a third-party library — and it had already gone wrong once: the
// block used to be numbered one too high for every section-shaped tag, so
// checkTags never recursed into a `{{#name}}` body and a real Section was
// classified as a plain variable.
//
// This asserts the CLASSIFICATION, taken from tags the library itself parsed,
// so it survives the switch to mustache's own constants and would go red on
// any renumbering.
func TestMustacheTagClassification(t *testing.T) {
	cases := []struct {
		name          string
		src           string
		wantType      mustache.TagType
		wantHasChild  bool
		wantIsSection bool
	}{
		{name: "escaped variable", src: "{{v}}", wantType: mustache.Variable},
		{name: "raw variable braces", src: "{{{v}}}", wantType: mustache.Variable},
		{name: "raw variable ampersand", src: "{{&v}}", wantType: mustache.Variable},
		{name: "section", src: "{{#s}}body{{/s}}", wantType: mustache.Section, wantHasChild: true, wantIsSection: true},
		{name: "inverted section", src: "{{^s}}body{{/s}}", wantType: mustache.InvertedSection, wantHasChild: true, wantIsSection: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := mustache.ParseString(tc.src)
			require.NoError(t, err)
			tags := tmpl.Tags()
			require.Len(t, tags, 1, "fixture must parse to exactly one top-level tag")

			// The fixture is only hostile if the library really reports
			// the type this case is about — otherwise every arm below is
			// asserting against the same tag shape.
			require.Equal(t, tc.wantType, tags[0].Type(),
				"fixture %q did not parse to the tag type this case tests", tc.src)

			assert.Equal(t, tc.wantHasChild, hasChildTags(tags[0].Type()),
				"only section-shaped tags may be walked for children")

			// A section name must be treated as a section, never as an
			// undefined plain variable — that is the polarity flip the old
			// numbering caused.
			literals := undefinedPlainVariableLiterals(tags, map[string]string{})
			if tc.wantIsSection {
				assert.NotContains(t, literals, "s",
					"a section name must not be emitted as an undefined plain-variable literal")
			} else {
				assert.Contains(t, literals, "v",
					"an undefined plain variable must be preserved as a literal")
			}
		})
	}

	// The nested case is what the historical off-by-one actually broke:
	// checkTags never recursed into a section body, so nested undefined
	// variables went unwarned.
	t.Run("checkTags recurses into a section body", func(t *testing.T) {
		tmpl, err := mustache.ParseString("{{#outer}}{{inner_name}}{{/outer}}")
		require.NoError(t, err)

		var warnings []string
		checkTags(tmpl.Tags(), map[string]string{}, collections.NewSet[string](),
			func(w string) { warnings = append(warnings, w) })

		assert.Contains(t, warnings, "undefined variable: {{inner_name}}",
			"a variable nested inside a section must be reached")
	})
}
