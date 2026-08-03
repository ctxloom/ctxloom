package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// walkCommands visits cmd and every command beneath it.
func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, visit)
	}
}

// TestGroupNodes_AreAllGuarded is the gate that keeps the fix from decaying.
//
// A command that HAS subcommands but is NOT runnable is exactly the shape
// cobra answers with "print help, exit 0" for any argument at all — see
// groupNode's doc for the two cobra behaviours that compose into that. There
// were 22 such nodes under the root; there must now be none, and a new
// namespace added without groupNode() reopens the hole in one line.
//
// The ROOT is the deliberate exception: cobra's own Find/legacyArgs already
// errors on an unknown top-level command, which is why `ctxloom bundel` always
// exited 1 while `ctxloom bundle lst` exited 0.
func TestGroupNodes_AreAllGuarded(t *testing.T) {
	var unguarded []string
	walkCommands(rootCmd, func(c *cobra.Command) {
		if c == rootCmd {
			return
		}
		if c.HasSubCommands() && !c.Runnable() {
			unguarded = append(unguarded, c.CommandPath())
		}
	})
	assert.Empty(t, unguarded,
		"these namespaces print help and exit 0 for ANY unknown subcommand; wrap the declaration in groupNode(): %v",
		unguarded)
}

// TestGroupNodes_UnknownSubcommandFails drives every namespace in the tree
// with a subcommand name nothing could plausibly own, and requires a real
// error. Structure (the test above) says the guard is installed; this says the
// guard WORKS, through cobra's actual dispatch rather than by calling the RunE
// directly.
//
// The bogus name is deliberately far from every real verb so no namespace's
// suggestion machinery matters here; the message shape is pinned separately in
// TestUnknownSubcommandError_Message.
func TestGroupNodes_UnknownSubcommandFails(t *testing.T) {
	var groups []*cobra.Command
	walkCommands(rootCmd, func(c *cobra.Command) {
		if c != rootCmd && c.HasSubCommands() {
			groups = append(groups, c)
		}
	})
	require.NotEmpty(t, groups, "the command tree has namespaces to check")

	for _, g := range groups {
		t.Run(g.CommandPath(), func(t *testing.T) {
			args := append(strings.Fields(g.CommandPath())[1:], "zzznotasubcommand")

			var out bytes.Buffer
			rootCmd.SetOut(&out)
			rootCmd.SetErr(&out)
			rootCmd.SetArgs(args)
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
			})

			err := rootCmd.Execute()

			require.Error(t, err,
				"%s zzznotasubcommand must fail, not print help and exit 0 (output was: %s)",
				g.CommandPath(), out.String())
			assert.Contains(t, err.Error(), "zzznotasubcommand",
				"the error names the verb the caller actually typed")
		})
	}
}

// TestUnknownSubcommandError_Message pins what the user reads: the mistyped
// verb, the namespace it was aimed at, the near-miss when there is one, and
// where to look for the real list. A message that named none of those would
// still exit 1 and still be useless.
func TestUnknownSubcommandError_Message(t *testing.T) {
	err := unknownSubcommandError(manageCmd, "instal")
	require.Error(t, err)
	msg := err.Error()

	assert.Contains(t, msg, `unknown command "instal"`, "names what was typed")
	assert.Contains(t, msg, `"ctxloom manage"`, "names the namespace it was aimed at")
	assert.Contains(t, msg, "Did you mean this?", "a one-character typo earns a suggestion")
	assert.Contains(t, msg, "install", "and the suggestion is the real verb")
	assert.Contains(t, msg, "ctxloom manage --help", "points at the full list")
}

// TestGroupNode_BareInvocationStillPrintsHelp pins the half that must NOT
// change. Asking a namespace what it holds by naming it alone is legitimate
// and stays a success; only a named verb that does not exist became an error.
func TestGroupNode_BareInvocationStillPrintsHelp(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"manage"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	require.NoError(t, rootCmd.Execute(), "bare `ctxloom manage` is a legitimate request for its help")
	assert.Contains(t, out.String(), "Available Commands:", "and it answers with the namespace's help")
}
