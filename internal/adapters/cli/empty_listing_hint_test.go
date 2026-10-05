package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestEmptyListing_OutsideAProject_SuggestsInit: with no project marker the
// config resolves to the home fallback, where an empty listing is not "you
// have added nothing yet" but "there is nothing to add to". The hint is the
// command that makes a project, not one that edits a profile in it.
func TestEmptyListing_OutsideAProject_SuggestsInit(t *testing.T) {
	testsupport.ProjectDir(t)
	resetApp()
	t.Cleanup(resetApp)

	for _, args := range [][]string{
		{"profile", "list"},
		{"command"},
		{"bundle", "list"},
	} {
		res := runCLI(t, append(args, "--format", formatText)...)
		require.NoError(t, res.err, "%v: %s", args, res.all())
		out := res.all()
		assert.Contains(t, out, noProjectListingHint, "%v", args)
		assert.False(t, strings.Contains(out, "profile create"), "%v: an edit command has no project to edit:\n%s", args, out)
	}
}

// TestEmptyListingHint_FollowsWhereTheConfigCameFrom: the home fallback means
// no project, so the hint is init; a project's empty listing keeps the
// add-content hint.
func TestEmptyListingHint_FollowsWhereTheConfigCameFrom(t *testing.T) {
	assert.Equal(t, noProjectListingHint, emptyListingHint(config.NewFixture(config.Fixture{Source: config.SourceHome})))
	assert.Equal(t, addContentListingHint, emptyListingHint(config.NewFixture(config.Fixture{Source: config.SourceProject})))
}
