package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelpAffordance_HelpFlagReachesEveryCommand is the "always present"
// invariant: --help reaches every command in the tree, leaves included.
//
// It is safe to assert everywhere precisely because a flag cannot collide with
// an operand. A bare `help` word would compete for the same slot as a
// positional, so on a leaf taking open-ended text one of the two has to lose;
// --help lives in a different namespace and takes nothing away from any
// command.
//
// The lookup is deliberately done WITHOUT executing anything. Cobra creates a
// local --help lazily on the command it is about to run, so a test that
// dispatched first would prove only that cobra initializes the node it
// reached. Finding the flag on an unexecuted node proves it was inherited from
// the root's persistent set, which is what makes it structural.
func TestHelpAffordance_HelpFlagReachesEveryCommand(t *testing.T) {
	root := rootCommand()

	var missing []string
	walkCommands(root, func(c *cobra.Command) {
		if c.InheritedFlags().Lookup("help") == nil && c.Flags().Lookup("help") == nil {
			missing = append(missing, c.CommandPath())
		}
	})

	assert.Empty(t, missing,
		"these commands do not inherit --help; it is a persistent flag on the root, so a gap means the command is detached from the root's flag chain: %v",
		missing)
}

// TestHelpAffordance_ShorthandStaysAvailable pins -h alongside it. The two are
// one affordance in users' hands, and a persistent --help that dropped the
// shorthand would silently retire the spelling most people type.
func TestHelpAffordance_ShorthandStaysAvailable(t *testing.T) {
	flag := rootCommand().PersistentFlags().Lookup("help")

	require.NotNil(t, flag, "--help is registered on the root's persistent flags")
	assert.Equal(t, "h", flag.Shorthand, "-h stays the shorthand for --help")
}

// TestHelpAffordance_NoBareHelpCommandAnywhere is the rule's gate: --help is
// the only spelling, so no node in the tree may be a `help` command.
//
// A `help` command that exists at some levels and not others is worse than one
// that exists nowhere, because the user cannot tell the levels apart without
// trying. This walks every node so the rule cannot hold "mostly": cobra
// auto-registers a help command on any command that has subcommands the moment
// it is executed, which means a single missed displacement reopens it.
func TestHelpAffordance_NoBareHelpCommandAnywhere(t *testing.T) {
	// Dispatch once before walking. Cobra grafts its help and completion
	// commands onto the tree during Execute, not at construction, so a walk
	// over a never-executed tree cannot see them — it would pass while
	// `ctxloom help` worked, and would flip to failing only when some other
	// test in the package happened to dispatch first.
	_, err := runRoot(t, "version")
	require.NoError(t, err)

	var found []string
	walkCommands(rootCommand(), func(c *cobra.Command) {
		if c.Name() == helpArgName {
			found = append(found, c.CommandPath())
		}
	})

	assert.Empty(t, found,
		"--help is the only spelling for help; these nodes are `help` commands: %v",
		found)
}

// TestHelpAffordance_BareHelpWordIsNotACommand asserts the same rule through
// real dispatch, which is the assertion that actually binds.
//
// Cobra's help command is displaced by handing it a hidden placeholder, and
// the placeholder is still ADDED to the tree — "hidden" is not "absent". Only
// driving the word proves a caller cannot reach it, so this must fail rather
// than print help and exit 0.
func TestHelpAffordance_BareHelpWordIsNotACommand(t *testing.T) {
	out, err := runRoot(t, helpArgName)

	require.Error(t, err,
		"`ctxloom help` must fail like any other word that is not a command (output was: %s)", out)
	assert.Contains(t, err.Error(), helpArgName,
		"the error names the word the caller typed")
}

// TestHelpAffordance_DoesNotLeakIntoTheNextCommand guards the hazard a
// PERSISTENT --help introduces that a per-command one cannot: one shared
// variable, read by every command in the process.
//
// Left set by an earlier invocation, it makes the next command print help and
// exit 0 while doing none of the work it was asked to do — a success status
// over an empty result, which is this project's characteristic failure and
// invisible to any exit-code-only assertion. Asserting on the OUTPUT is what
// catches it.
func TestHelpAffordance_DoesNotLeakIntoTheNextCommand(t *testing.T) {
	remoteBareFixture(t)

	_, err := runRoot(t, "remote", "--help")
	require.NoError(t, err)

	after, err := runRoot(t, "remote", "list")

	require.NoError(t, err)
	assert.NotContains(t, after, usageMarker,
		"a --help on one command must not turn the next one into a no-op that prints help")
}
