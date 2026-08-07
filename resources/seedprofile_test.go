package resources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestGetSeedProfile_DefaultParentsAreExactlyWhatInitSeeds pins what every new
// project inherits.
//
// This existed as an untested claim until 2026-08-07: the seeded profile's
// parent list is what `ctxloom init` composes into a user's very first
// context, and NOTHING asserted it. The only two references anywhere were a
// ref-PARSING fixture string in internal/remote and a file-exists check in
// j20 — so a parent could be removed, renamed, or pointed at a bundle that no
// longer exists, and every gate would stay green while new projects silently
// got different context.
//
// That is not hypothetical: the `default` bundle this profile used to inherit
// from was DELETED (its conduct fragments moved to a bundle named for what
// they hold), and the dangling parent had to be found by reading the file
// rather than by a failing test.
//
// The list is asserted EXACTLY rather than by substring. A parent added by
// accident is as much a change to what users receive as one removed, and only
// an exact set catches both.
func TestGetSeedProfile_DefaultParentsAreExactlyWhatInitSeeds(t *testing.T) {
	raw, err := GetSeedProfile("default")
	require.NoError(t, err, "the default seed profile must be embedded — init cannot scaffold without it")

	var got struct {
		Parents []string `yaml:"parents"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &got), "the seed profile must be valid YAML: init writes it verbatim")

	assert.Equal(t, []string{
		"https://github.com/ctxloom/ctxloom-default@bundles/ai-developer@branch:main#profiles/developer",
	}, got.Parents,
		"changing what a new project inherits is a deliberate act; update this expectation in the same change, and say why in the commit")
}

// TestGetSeedProfile_DefaultNamesNoDeletedBundle guards the specific failure
// that motivated the test above: a parent surviving the deletion of the bundle
// it points at. A dangling remote ref does not fail at scaffold time — it
// fails later, when resolution cannot find it, far from the edit that caused
// it.
func TestGetSeedProfile_DefaultNamesNoDeletedBundle(t *testing.T) {
	raw, err := GetSeedProfile("default")
	require.NoError(t, err)

	assert.NotContains(t, string(raw), "@bundles/default",
		"the `default` bundle was split into `conduct`; a seed still inheriting from it would dangle at resolution time")
}
