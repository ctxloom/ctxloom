package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// remoteBareFixture puts a scaffolded project under the test's cwd so the
// listing has a real config to read, and returns nothing: every assertion
// below is about what the CLI prints, not about the fixture.
func remoteBareFixture(t *testing.T) {
	t.Helper()
	root, _ := setupProject(t, "mock")
	t.Chdir(root)
}

// helpMarker is the heading cobra puts above a namespace's subcommand table.
// It appears in help output and in nothing else, which is what makes it a
// usable discriminator between "this namespace taught me" and "this namespace
// answered me".
const helpMarker = "Available Commands:"

// usageMarker is the heading cobra prints for EVERY command's help, leaf or
// namespace. helpMarker only appears on a namespace, so a leaf that was
// silently turned into a help print is invisible to it.
const usageMarker = "Usage:"

// TestGroupNodeDefault_NamesAChildThatExists checks every bare-noun delegate
// in the tree, not just the pilot's.
//
// The delegate is named by string and resolved at dispatch, so a typo or a
// renamed subcommand turns the bare noun into a runtime error that nothing
// notices until a user types it. This walk finds it at build time and covers
// each noun wired later without being edited.
func TestGroupNodeDefault_NamesAChildThatExists(t *testing.T) {
	var wired int
	walkCommands(rootCommand(), func(c *cobra.Command) {
		primary, ok := groupNodeDefaultChild(c)
		if !ok {
			return
		}
		wired++
		assert.NotNil(t, findSub(c, primary),
			"%s answers its bare form with %q, which is not one of its subcommands",
			c.CommandPath(), primary)
	})
	assert.Positive(t, wired, "at least one namespace answers its bare form with a listing")
}

// TestRemoteBare_ListsRatherThanTeaches is the pilot for the bare-noun rule.
// `ctxloom remote` answers with the registry, the way `git remote` does.
//
// Equality against `remote list` is the assertion that bites. Checking only
// that the output "looks like a listing" would pass against a listing that
// dropped rows, and checking only that it is non-empty passes against help.
func TestRemoteBare_ListsRatherThanTeaches(t *testing.T) {
	remoteBareFixture(t)

	bare, err := runRoot(t, "remote")
	require.NoError(t, err)
	listed, err := runRoot(t, "remote", "list")
	require.NoError(t, err)

	assert.Equal(t, listed, bare,
		"bare `ctxloom remote` is the same entry point as `ctxloom remote list`")
	assert.NotContains(t, bare, usageMarker,
		"bare `ctxloom remote` answers with the registry; help has its own spelling")
	assert.Contains(t, bare, "remote",
		"the listing names the resource it is a listing of")
}

// TestRemoteBare_ListSubcommandAndAliasStillWork holds the promise that the
// bare form is an ADDITIONAL entry point. Nothing a user has typed before
// stops working, including the `ls` alias.
func TestRemoteBare_ListSubcommandAndAliasStillWork(t *testing.T) {
	remoteBareFixture(t)

	listed, err := runRoot(t, "remote", "list")
	require.NoError(t, err)
	aliased, err := runRoot(t, "remote", "ls")
	require.NoError(t, err)

	assert.NotContains(t, listed, usageMarker, "`remote list` lists")
	assert.Equal(t, listed, aliased, "`ls` stays an alias of `list`")
}

// TestRemoteBare_HelpSuffixTeaches is the other half of the trade: the bare
// form gave up teaching, so the explicit spelling must deliver it.
func TestRemoteBare_HelpSuffixTeaches(t *testing.T) {
	remoteBareFixture(t)

	out, err := runRoot(t, "remote", "help")

	require.NoError(t, err)
	assert.Contains(t, out, helpMarker, "`ctxloom remote help` teaches")
	assert.Contains(t, out, usageAnchor("remote"), "it teaches about remote specifically")
}

// TestRemoteBare_HelpFlagsUntouched pins the flags as untouched. They are the
// spelling every other CLI trains users on, and the new suffix is an addition
// to them rather than a replacement.
func TestRemoteBare_HelpFlagsUntouched(t *testing.T) {
	remoteBareFixture(t)

	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			out, err := runRoot(t, "remote", flag)

			require.NoError(t, err)
			assert.Contains(t, out, helpMarker, "`ctxloom remote %s` still prints help", flag)
		})
	}
}
