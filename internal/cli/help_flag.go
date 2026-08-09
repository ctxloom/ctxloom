package cli

import (
	"github.com/spf13/cobra"
)

// installHelpFlag puts --help on the ROOT's persistent flag set, which is what
// makes the flag reach every command in the tree by inheritance.
//
// --help IS THE ONLY WAY TO ASK FOR HELP, at every level. There is no `help`
// word anywhere in the tree (see disableHelpCommand): an affordance present on
// some commands and absent on others is one a user has to carry a model of,
// and the value of a single spelling is that it needs no model. A flag is the
// spelling that can be universal without exception, because it occupies a
// different namespace from operands and so competes with nothing a command
// takes positionally — `ctxloom search help` searches for the word "help",
// and `ctxloom bundle create help` creates a bundle called "help", while
// --help on either one teaches.
//
// Cobra would otherwise give each command a LOCAL --help, created lazily on
// the one command being executed. Inheritance from a single declaration is
// stronger: a command added tomorrow carries the flag by construction, and
// TestHelpAffordance_HelpFlagReachesEveryCommand can see it on the assembled
// tree without executing anything.
func installHelpFlag(root *cobra.Command) {
	if root.PersistentFlags().Lookup("help") != nil {
		return
	}
	root.PersistentFlags().BoolP("help", "h", false, "show help for this command")
}

// resetHelpFlag puts --help back to false before a dispatch.
//
// The flag asks a question about ONE invocation, and a persistent flag is a
// single shared variable rather than a fresh one per command. Left set, it
// answers for every later dispatch in the same process: the command prints
// help, exits 0, and does none of the work it was asked to do — a success
// status over nothing. Any process that dispatches more than once (the test
// harness today) reads a stale answer without it.
func resetHelpFlag(root *cobra.Command) {
	flag := root.PersistentFlags().Lookup("help")
	if flag == nil {
		return
	}
	_ = flag.Value.Set("false")
	flag.Changed = false
}

// disableHelpCommand takes cobra's auto-registered `help` command out of the
// tree, so `ctxloom help` resolves to nothing and --help is left as the single
// spelling.
//
// Cobra grafts its help command on inside Execute, AFTER every hook this
// package controls, so there is no point at which it can simply be removed —
// InitDefaultHelpCmd re-adds whatever helpCommand holds on every dispatch.
// Handing it a placeholder is the supported way to displace the real one: the
// placeholder is what gets added, and it carries no name for a caller to
// reach. `ctxloom help` then lands on the root's unknown-command path and
// fails, which is the same answer any other word that is not a command gets.
//
// TestHelpAffordance_BareHelpWordIsNotACommand asserts the OUTCOME through
// real dispatch rather than trusting this call, because the placeholder is
// still added to the tree and "hidden" is not "absent".
func disableHelpCommand(root *cobra.Command) {
	root.SetHelpCommand(&cobra.Command{Hidden: true})
}
