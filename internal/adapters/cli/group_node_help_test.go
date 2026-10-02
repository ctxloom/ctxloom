package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGroupNodes_UnknownSubcommandFailsUnderHelp is the --help half of
// TestGroupNodes_UnknownSubcommandFails. A probe of the form `<cmd> --help`
// is how a script or agent asks whether a command exists, so a namespace that
// answered a verb it does not have with its own help and a 0 exit confirmed
// every nonexistent subcommand in the tree.
//
// It asserts the EFFECT on both channels: the error names the verb and lists
// the real ones, and the namespace's help is NOT printed — help followed by a
// refusal would still read, to anything parsing stdout, as the command
// answering.
func TestGroupNodes_UnknownSubcommandFailsUnderHelp(t *testing.T) {
	var groups []*cobra.Command
	walkCommands(rootCmd, func(c *cobra.Command) {
		if c != rootCmd && c.HasSubCommands() {
			groups = append(groups, c)
		}
	})
	require.NotEmpty(t, groups, "the command tree has namespaces to check")

	for _, g := range groups {
		for _, helpFlag := range []string{"--help", "-h"} {
			t.Run(g.CommandPath()+" "+helpFlag, func(t *testing.T) {
				args := append(strings.Fields(g.CommandPath())[1:], "zzznotasubcommand", helpFlag)

				out, err := runRoot(t, args...)

				require.ErrorIs(t, err, ErrUnknownSubcommand,
					"%s zzznotasubcommand %s must fail, not print help and exit 0 (output was: %s)",
					g.CommandPath(), helpFlag, out)
				assert.Contains(t, err.Error(), `"zzznotasubcommand"`,
					"the error names the verb the caller actually typed")
				assert.NotContains(t, out, "Available Commands:",
					"the namespace's help must not be printed for a verb it does not have")
			})
		}
	}
}

// TestGroupNode_HelpOnRealCommandsStillSucceeds pins the half that must not
// change: --help on a namespace itself, and on a verb it really has, is a
// legitimate question and stays a success.
func TestGroupNode_HelpOnRealCommandsStillSucceeds(t *testing.T) {
	for _, args := range [][]string{
		{"manage", "--help"},
		{"manage", "-h"},
		{"bundle", "--help"},
		{"bundle", "list", "--help"},
		{"init", "--help"},
		// A leaf's positional arguments are operands, not verbs: even one its
		// validator would refuse does not turn its --help into a refusal.
		{"init", "prompt", "zzznotanoperand", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := runRoot(t, args...)

			require.NoError(t, err)
			assert.Contains(t, out, "Usage:", "it answers with help")
		})
	}
}

// TestUnknownSubcommandError_ListsRealSubcommands pins the ruling's third
// clause: the refusal names the parent's real verbs, so the caller does not
// need a second round trip to find the one they meant. A hidden or deprecated
// verb stays out of the list — it is what --help would show, no more.
func TestUnknownSubcommandError_ListsRealSubcommands(t *testing.T) {
	var listed, withheld int
	walkCommands(rootCmd, func(parent *cobra.Command) {
		if parent == rootCmd || !parent.HasSubCommands() {
			return
		}
		err := unknownSubcommandError(parent, "zzznotasubcommand")
		require.ErrorIs(t, err, ErrUnknownSubcommand)
		for _, sub := range parent.Commands() {
			line := "\n  " + sub.Name() + "\n"
			if sub.IsAvailableCommand() {
				listed++
				assert.Contains(t, err.Error(), line, "%s lists its real verb %q", parent.CommandPath(), sub.Name())
				continue
			}
			withheld++
			assert.NotContains(t, err.Error(), line, "%s does not list the unavailable verb %q", parent.CommandPath(), sub.Name())
		}
	})
	require.Positive(t, listed, "the tree has verbs to list")
	require.Positive(t, withheld, "the tree has a hidden or deprecated verb, so withholding one is actually exercised")
}
