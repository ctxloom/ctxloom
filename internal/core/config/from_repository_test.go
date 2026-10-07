package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// A bundle fetched from a repository — over git or from a file:// one — holds
// its shipped profiles to that repository; a local or companion bundle has no
// repository to hold them to.
func TestFromRepository_OnlyRepositorySources(t *testing.T) {
	mint := func(br ident.BundleRef, err error) ident.BundleRef {
		t.Helper()
		require.NoError(t, err)
		return br
	}
	for _, tc := range []struct {
		name string
		src  ident.BundleRef
		want bool
	}{
		{"git", mint(ident.GitRef("github.com", "acme/repo", "kit")), true},
		{"file", mint(ident.FileRef("/srv/repo", "kit")), true},
		{"local", mint(ident.LocalRef("kit")), false},
		{"companion", mint(ident.CompanionRef("ltk")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, fromRepository(tc.src))
		})
	}
}
