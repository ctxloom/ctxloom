package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// CanonicalSpelling moves a fetch-address bundle ref onto the canonical URI
// grammar, keeping its version pin and item selector, and leaves every other
// spelling as written — so it is safe to hand any stored ref.
func TestCanonicalSpelling(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"fetch address":             {"https://github.com/o/r@bundles/core", "ctxloom+git://github.com/o/r//bundles/core"},
		"with selector":             {"https://github.com/o/r@bundles/kit#profiles/dev", "ctxloom+git://github.com/o/r//bundles/kit#profiles/dev"},
		"with version pin":          {"https://github.com/o/r@bundles/core@abc1234", "ctxloom+git://github.com/o/r//bundles/core@abc1234"},
		"already canonical":         {"ctxloom+git://github.com/o/r//bundles/core", "ctxloom+git://github.com/o/r//bundles/core"},
		"local":                     {"ctxloom:local@bundles/x", "ctxloom:local@bundles/x"},
		"bare local bundle":         {"core-practices", "core-practices"},
		"short alias form":          {"personal/kit", "personal/kit"},
		"url naming no bundle path": {"https://github.com/o/r", "https://github.com/o/r"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, CanonicalSpelling(tc.in))
		})
	}
}
