package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// groupNode marks cmd as a pure GROUP node — a namespace whose only job is to
// hold subcommands — and gives it the one behaviour cobra will not: an unknown
// subcommand FAILS.
//
// THE DEFECT IT FIXES. `ctxloom manage confgi show` used to print manage's own
// help and exit 0. So did every other misspelling under every other namespace:
// 22 of them, the whole command surface below the root. Two integration tests
// (TestConfig_Show, TestConfig_Get) drove `manage config show` — a subtree that
// was removed when configuration moved to the top-level `config` command — and
// passed for their entire life while exercising nothing at all, because an
// exit-code-only assertion cannot tell "the command ran" from "cobra printed
// help". That is this project's characteristic silent-no-op shape, this time in
// the CLI's own dispatch.
//
// WHY `Args:` IS NOT THE FIX, though it is the obvious reach. Two cobra
// behaviours compose into the hole:
//
//  1. (*Command).Find validates leftover args with legacyArgs ONLY while the
//     found command's Args is nil, and legacyArgs deliberately reports an
//     unknown command for the ROOT alone — "root command with subcommands, do
//     subcommand checking" — returning nil for any command with a parent.
//  2. (*Command).execute returns flag.ErrHelp for any !Runnable() command
//     BEFORE it ever calls ValidateArgs, and ExecuteC treats ErrHelp as
//     success: print help, return nil.
//
// So setting Args on a non-runnable group node is worse than useless — it makes
// Find skip legacyArgs, and execute then bails at (2) before the validator
// runs. Being RUNNABLE is what puts a node on a path where its own arguments
// are ever looked at, which is why the guard is a RunE.
//
// WHAT IT DOES NOT CHANGE. A bare `ctxloom manage` still prints help and exits
// 0 — that is a legitimate way to ask what a namespace holds, and the only
// invocation that reaches this RunE with no arguments. `--help` never reaches
// here at all (cobra intercepts it earlier). Only a NAMED subcommand that does
// not exist changes outcome, from 0 to 1.
//
// Applied at each declaration site rather than by walking the tree, because a
// walk would have to run after every AddCommand in every init() in the package
// and Go does not order those for you. TestArch_GroupNodesFailOnUnknownSubcommand
// is what keeps the 22 sites from becoming 21.
func groupNode(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[groupNodeAnnotation] = "true"
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 0 {
			return c.Help()
		}
		return unknownSubcommandError(c, args[0])
	}
	return cmd
}

// groupNodeAnnotation marks a command as carrying only the groupNode guard.
//
// The guard makes a namespace Runnable() — that is the whole mechanism — and
// cobra.Command exposes no way to ask "runnable only because of the guard".
// Tree walkers that mean "every command that DOES something" (the --format
// coverage registry, and anything like it) would otherwise start demanding a
// per-namespace entry for a RunE whose entire behaviour is printing help or
// refusing a typo. isGroupNode is how they ask.
const groupNodeAnnotation = "ctxloom.group-node"

// isGroupNode reports whether cmd is a namespace whose RunE is nothing but the
// unknown-subcommand guard.
func isGroupNode(cmd *cobra.Command) bool {
	return cmd.Annotations[groupNodeAnnotation] == "true"
}

// unknownSubcommandError is the message a mistyped namespace verb earns. It
// mirrors the wording cobra already produces for the root command, so
// `ctxloom bundel` and `ctxloom bundle lst` read the same way, and appends the
// pointer that actually resolves the situation: the namespace's own help lists
// every verb it has.
func unknownSubcommandError(cmd *cobra.Command, name string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown command %q for %q", name, cmd.CommandPath())
	if suggestions := cmd.SuggestionsFor(name); len(suggestions) > 0 {
		b.WriteString("\n\nDid you mean this?\n")
		for _, s := range suggestions {
			fmt.Fprintf(&b, "\t%v\n", s)
		}
	}
	fmt.Fprintf(&b, "\n\nRun '%s --help' for the subcommands it has.", cmd.CommandPath())
	return fmt.Errorf("%s", b.String())
}
