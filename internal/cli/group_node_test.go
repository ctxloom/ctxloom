package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// walkCommands visits cmd and every command beneath it.
func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, visit)
	}
}

// runRoot drives the real rootCmd with args and returns everything it wrote
// plus its error.
//
// rootCmd is package-global and its persistent flags KEEP whatever the last
// test set — --format in particular, which other suites in this package leave
// on markdown. That is not this test's state to inherit, so the flag is put
// back to its default here as well as after; a namespace test that quietly
// depended on a neighbour's leftovers would be measuring the neighbour.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	resetFormat := func() {
		if f := rootCmd.PersistentFlags().Lookup("format"); f != nil {
			require.NoError(t, f.Value.Set(f.DefValue))
			f.Changed = false
		}
	}
	resetFormat()
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		resetFormat()
		// rootPersistentPreRun flips clidiag's structured channel on for a
		// json/yaml/toml --format and nothing ever flips it back; a later
		// test asserting on a plain "ctxloom: warning: ..." line would then
		// find structured output and fail for a reason that has nothing to
		// do with it. Process-global state set by an Execute() is this
		// helper's to undo.
		clidiag.SetStructured(false)
	})
	err := rootCommand().Execute()
	return out.String(), err
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

// TestGroupNodes_MarkAndStructureAgree shuts the door the group-node
// annotation opens. Tree walkers that mean "commands that DO something" skip
// anything isGroupNode reports true for — the --format coverage registry is
// one — so marking a LEAF would silently excuse a real command from those
// gates without deleting a single assertion. A group node has subcommands, by
// definition; nothing else may claim to be one.
func TestGroupNodes_MarkAndStructureAgree(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		if isGroupNode(c) {
			assert.True(t, c.HasSubCommands(),
				"%s is marked a group node but holds no subcommands — groupNode() is not a way out of a coverage gate",
				c.CommandPath())
		}
	})
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

			out, err := runRoot(t, args...)

			require.Error(t, err,
				"%s zzznotasubcommand must fail, not print help and exit 0 (output was: %s)",
				g.CommandPath(), out)
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
	out, err := runRoot(t, "manage")

	require.NoError(t, err, "bare `ctxloom manage` is a legitimate request for its help")
	assert.Contains(t, out, "Available Commands:", "and it answers with the namespace's help")
}

// TestGroupNode_BareInvocationRejectsFormat is the opposite side of the test
// above, and the pair is the whole contract: a namespace named alone prints
// help and succeeds; a namespace named alone WITH a machine-readable --format
// fails.
//
// Asking for json and receiving prose with a 0 exit is the same silent no-op
// as a mistyped subcommand reporting success — nothing in the status tells the
// caller it got something it cannot parse.
//
// The refusal replaces the help output rather than following it, which is why
// it lives in groupNode's RunE and not in checkFormatWasHonored: cobra runs
// that hook only after a successful RunE, so siting it there would print the
// whole help text and then refuse it.
func TestGroupNode_BareInvocationRejectsFormat(t *testing.T) {
	for _, format := range []string{"json", "yaml", "toml", "markdown"} {
		t.Run(format, func(t *testing.T) {
			out, err := runRoot(t, "manage", "--format", format)

			require.Error(t, err, "--format %s on a namespace must fail, not print prose and exit 0", format)
			assert.Contains(t, err.Error(), "cannot be honored by a command group")
			assert.Contains(t, err.Error(), "name a subcommand", "the message says what to do instead")
			assert.NotContains(t, out, "Available Commands:",
				"the refusal replaces the help dump; printing prose AND refusing it is the worst of both")
		})
	}
}

// TestGroupNode_ExplicitTextFormatStillPrintsHelp keeps `--format text` on the
// success side. text is what help already is, so nothing is being silently
// dropped — the refusal is about encodings that cannot carry prose, not about
// the flag appearing at all.
func TestGroupNode_ExplicitTextFormatStillPrintsHelp(t *testing.T) {
	out, err := runRoot(t, "manage", "--format", "text")

	require.NoError(t, err, "`--format text` asks for exactly what help is")
	assert.Contains(t, out, "Available Commands:")
}

// TestGroupNode_UnresolvableFormatIsRefused covers the value that parses as a
// flag but names no encoding. Every other command reports this from its own
// emit() call, which is why checkFormatWasHonored lets an unresolvable value
// through; a namespace never calls emit(), so without this it would exit 0
// having ignored the flag entirely.
func TestGroupNode_UnresolvableFormatIsRefused(t *testing.T) {
	_, err := runRoot(t, "manage", "--format", "zzznotaformat")

	require.Error(t, err, "an unknown --format value on a namespace must fail")
	assert.Contains(t, err.Error(), "zzznotaformat")
}

// TestGroupNode_PersistentFormatAheadOfSubcommandStillWorks is the guard on
// the guard, and the one that would hurt most to get wrong. --format is a
// persistent ROOT flag, so `ctxloom --format json manage check` is an
// ordinary scripted invocation: the flag is typed ahead of a namespace, but
// the command it lands on is `manage check`, which honors json. The refusal
// keys on the INVOKED command, never on where the flag was typed — otherwise
// every script that sets --format globally would break at once.
func TestGroupNode_PersistentFormatAheadOfSubcommandStillWorks(t *testing.T) {
	for _, args := range [][]string{
		{"--format", "json", "manage", "check"},
		{"manage", "--format", "json", "check"},
		{"manage", "check", "--format", "json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := runRoot(t, args...)

			require.NoError(t, err,
				"--format json reaches `manage check`, which honors it, whatever the flag's position")
			assert.Contains(t, out, "{", "and the payload really is json")
		})
	}
}
