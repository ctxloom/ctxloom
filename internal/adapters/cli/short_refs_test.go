package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
)

var shortRefRemotes = []operations.RemoteEntry{
	{Name: "ctxloom-default", URL: "https://github.com/ctxloom/ctxloom-default"},
	{Name: "acme", URL: "https://github.com/acme/content"},
}

func aliasURL(alias string) string {
	for _, r := range shortRefRemotes {
		if r.Name == alias {
			return r.URL
		}
	}
	return ""
}

// TestShortRefs_RenderWhatUsersType: a remote item's canonical ref is shown,
// in text, as "<remote>/<bundle>[#...]" — the spelling `profile list` told the
// user to type — and that spelling expands back to the very same ref through
// the one short-ref grammar (remote.CanonicalizeShortRef). Anything no
// registered remote names (a local bundle, an unregistered host) is unchanged.
func TestShortRefs_RenderWhatUsersType(t *testing.T) {
	canon := func(short string) string { return remote.CanonicalizeShortRef(short, aliasURL, nil) }
	refs := []string{
		canon("ctxloom-default/ai-developer"),
		canon("ctxloom-default/ai-developer#profiles/developer"),
		canon("acme/go#fragments/style"),
		"project",
		"ctxloom+git://github.com/someone/else//bundles/x",
	}

	got := shortRefs(refs, shortRefRemotes)

	assert.Equal(t, []string{
		"ctxloom-default/ai-developer",
		"ctxloom-default/ai-developer#profiles/developer",
		"acme/go#fragments/style",
		"project",
		"ctxloom+git://github.com/someone/else//bundles/x",
	}, got)
	for i, short := range got {
		assert.Equal(t, refs[i], remote.CanonicalizeShortRef(short, aliasURL, nil), "the short form must round-trip: %q", short)
	}
}

// TestShortRefs_SharedShortNameFallsBackToCanonical: two refs that would be
// shown under one label are each shown canonically, so neither label names
// something it does not resolve to.
func TestShortRefs_SharedShortNameFallsBackToCanonical(t *testing.T) {
	a := remote.CanonicalizeShortRef("acme/go", aliasURL, nil)
	refs := []string{a, "acme/go"} // a local item spelled like the short form
	assert.Equal(t, []string{a, "acme/go"}, shortRefs(refs, shortRefRemotes))
}
