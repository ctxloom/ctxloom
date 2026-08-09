package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
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
// invocation that reaches this RunE with no arguments and nothing to refuse.
// `--help` never reaches here at all (cobra intercepts it earlier). Exactly two
// outcomes go from 0 to 1: a NAMED subcommand that does not exist, and a
// machine-readable --format aimed at the namespace itself
// (groupNodeFormatRefusal).
//
// Applied at each declaration site rather than by walking the tree, because a
// walk would have to run after every AddCommand in every init() in the package
// and Go does not order those for you. TestGroupNodes_AreAllGuarded is what
// keeps the 22 sites from becoming 21.
func groupNode(cmd *cobra.Command) *cobra.Command {
	markGroupNode(cmd)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return unknownSubcommandError(c, args[0])
		}
		if err := groupNodeFormatRefusal(c); err != nil {
			return err
		}
		return c.Help()
	}
	return cmd
}

// groupNodeDefault is groupNode for a namespace that has a safe default view of
// what it holds: the bare form runs the named child instead of printing help.
// `ctxloom remote` lists remotes, the way `git remote` and `systemctl` do.
//
// WHY THE BARE FORM ANSWERS RATHER THAN TEACHES. The most-typed spelling should
// answer the most common question, and someone typing a noun on its own almost
// always wants to know what they have. The teaching surface is not lost, it is
// spelled explicitly: installHelpSuffix puts `help` on every namespace, so
// `ctxloom remote help` is always there for the caller who wants it.
//
// THE CHILD MUST BE READ-ONLY AND SIDE-EFFECT-FREE. This runs on a bare noun,
// which is the invocation a caller types while exploring, so anything it can
// reach must be safe to reach by accident. A namespace with no such view keeps
// plain groupNode and falls back to help, which still answers with something
// worth having; erroring on a bare noun is never the answer.
//
// --format is NOT refused here, unlike plain groupNode: the delegate produces a
// real payload, and its own emit() honors the flag.
func groupNodeDefault(cmd *cobra.Command, primary string) *cobra.Command {
	markGroupNode(cmd)
	cmd.Annotations[groupNodeDefaultAnnotation] = primary
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return unknownSubcommandError(c, args[0])
		}
		return runGroupNodeDefault(c, primary)
	}
	return cmd
}

// runGroupNodeDefault dispatches a bare namespace to its default child.
//
// The child is looked up by name at call time rather than captured at
// declaration time: the namespace variable is constructed before any init()
// adds children to it, so at declaration there is nothing to capture.
//
// The context is carried across explicitly. Cobra sets the context on the
// command it dispatched to — the namespace — and the child, never having been
// dispatched to, would otherwise run with none.
func runGroupNodeDefault(cmd *cobra.Command, primary string) error {
	for _, child := range cmd.Commands() {
		if child.Name() != primary {
			continue
		}
		child.SetContext(cmd.Context())
		if child.PreRunE != nil {
			if err := child.PreRunE(child, nil); err != nil {
				return err
			}
		}
		return child.RunE(child, nil)
	}
	return fmt.Errorf("%s: no %q subcommand to answer the bare form", cmd.CommandPath(), primary)
}

// markGroupNode records that cmd is a namespace, which is what lets tree
// walkers tell a namespace's RunE from a command that does work.
func markGroupNode(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[groupNodeAnnotation] = "true"
}

// groupNodeDefaultAnnotation names the child a bare namespace runs, so tests
// can check the delegate exists and is safe without invoking it.
const groupNodeDefaultAnnotation = "ctxloom.group-node-default"

// groupNodeDefaultChild returns the child cmd's bare form runs, and whether it
// has one.
func groupNodeDefaultChild(cmd *cobra.Command) (string, bool) {
	name, ok := cmd.Annotations[groupNodeDefaultAnnotation]
	return name, ok
}

// groupNodeFormatRefusal rejects a machine-readable --format aimed at a
// namespace, and is the second half of the same principle as the guard above.
// `ctxloom manage --format json` used to print manage's help — prose — and
// exit 0: a caller that asked for json to parse got something it cannot parse,
// with nothing in the status to say so. Same silent-no-op shape as the
// unknown subcommand, same answer.
//
// Help is not a payload and no encoding will ever carry it, so unlike
// checkFormatWasHonored's error this promises no tracked fix — the only thing
// that produces a payload is a subcommand, which is what the message asks for.
//
// ONLY A TERMINAL NAMESPACE, which is the distinction that matters most here.
// --format is a persistent ROOT flag, so it may legitimately be typed anywhere;
// what decides the outcome is the command it lands on, never the flag's
// position. `ctxloom --format json manage status` runs `manage status`, whose
// own RunE honors json — this never sees it. Only a namespace as the ENTIRE
// command reaches this.
//
// It lives in the RunE rather than in checkFormatWasHonored (rootCmd's
// PersistentPostRunE, the natural-looking home) for two reasons: cobra runs
// that hook only AFTER a successful RunE, so the caller would read the whole
// help text and then be told it was refused; and keeping it here leaves the
// shared format guard exactly as it was for every other command.
//
// An unresolvable value (`--format bogus`) is refused too. Every other command
// reports that from its own emit() call, which is why checkFormatWasHonored
// lets it pass; a namespace never calls emit(), so nothing else would, and it
// would exit 0 having ignored the flag twice over.
func groupNodeFormatRefusal(cmd *cobra.Command) error {
	format, err := cliemit.Resolve(cmd)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd.CommandPath(), err)
	}
	if format == clifmt.FormatText {
		return nil
	}
	return fmt.Errorf("%s: --format %s cannot be honored by a command group: %s prints help, and help is not a payload any encoding can carry — name a subcommand that produces one (see '%s --help')",
		cmd.CommandPath(), format, cmd.CommandPath(), cmd.CommandPath())
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
