package cli

import (
	"github.com/spf13/cobra"
)

// installHelpSuffix gives every namespace in root's tree a `help` child, so
// `<anything> help` teaches about that thing at every depth: `ctxloom help`,
// `ctxloom remote help`, `ctxloom trust signer help`, `ctxloom mcp help`.
//
// UNIVERSALITY IS THE POINT, AND IT IS WHY THIS IS A WALK. A bare namespace
// answers with a listing rather than help (see groupNodeDefault), so the
// teaching surface lives entirely in this suffix. A newcomer cannot know in
// advance which commands are generous, so an affordance present on most of
// them is one they still have to test for — it fails exactly where it is
// needed. Registering `help` by hand per command is a convention that lapses
// the first time a namespace is added by someone who has not read this; one
// walk over the assembled tree cannot lapse, and a new namespace inherits the
// suffix without its author doing anything.
//
// It runs after cobra's own lazy initialization rather than before, because
// InitDefaultHelpCmd and InitDefaultCompletionCmd graft commands onto the tree
// at Execute() time; walking first would leave whatever they add uncovered.
// Both are idempotent, so calling them here only moves them earlier.
//
// Namespaces only. A leaf has no subcommand slot to put `help` in, and giving
// it one would shadow a positional argument that legitimately reads "help" —
// `ctxloom bundle create help` names the bundle it creates. Leaves reach the
// same affordance through helpArgName instead.
func installHelpSuffix(root *cobra.Command) {
	installHelpFlag(root)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	var install func(cmd *cobra.Command)
	install = func(cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			install(sub)
		}
		if !cmd.HasSubCommands() {
			return
		}
		if existing := findHelpSuffix(cmd); existing != nil {
			// The root's own help command comes from cobra. Marking it makes
			// every help command in the tree answer isHelpSuffix, so walkers
			// that exempt them need no special case for the root.
			markHelpSuffix(existing)
			return
		}
		cmd.AddCommand(newHelpSuffixCommand(cmd))
	}
	install(root)
}

// installHelpFlag puts --help on the ROOT's persistent flag set, which is what
// makes the flag reach every command in the tree by inheritance.
//
// This is the affordance that is universal without exception. A flag occupies a
// different namespace from operands, so it competes with nothing a command
// takes positionally: `ctxloom search --help` teaches while `ctxloom search
// help` still searches for the word. That is precisely what a bare `help`
// suffix cannot do on a leaf, which is why the suffix stops at namespaces.
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

// newHelpSuffixCommand builds the `help` child that teaches about parent.
//
// It resolves paths against PARENT, which is what cobra's own default help
// command cannot do: that one calls c.Root().Find(args), so a copy grafted
// onto a nested namespace answers `ctxloom remote help` with the ROOT's help.
// Anchoring at the parent makes the suffix compose down the tree exactly as
// far as the commands do.
func newHelpSuffixCommand(parent *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   helpArgName + " [command]",
		Short: "Help about " + parent.Name() + " and its commands",
		Annotations: map[string]string{helpSuffixAnnotation: "true"},
		// Completion offers the sibling verbs this help command can describe.
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			var names []string
			for _, sub := range parent.Commands() {
				names = append(names, sub.Name())
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Help is prose, and no encoding carries prose as a payload, so a
			// machine-readable --format aimed here is refused rather than
			// answered with something the caller cannot parse.
			if err := groupNodeFormatRefusal(cmd); err != nil {
				return err
			}
			if len(args) == 0 {
				return parent.Help()
			}
			target, _, err := parent.Find(args)
			if err != nil {
				return unknownSubcommandError(parent, args[0])
			}
			return target.Help()
		},
	}
}

// helpSuffixAnnotation marks the `help` child installed on every namespace.
//
// The suffix commands are Runnable and visible, so tree walkers that mean
// "every command that produces a payload" — the --format coverage registry,
// and anything like it — would otherwise demand a per-namespace entry for a
// command whose entire output is help text. isHelpSuffix is how they ask.
const helpSuffixAnnotation = "ctxloom.help-suffix"

// isHelpSuffix reports whether cmd is a namespace's `help` child.
func isHelpSuffix(cmd *cobra.Command) bool {
	return cmd.Annotations[helpSuffixAnnotation] == "true"
}

// markHelpSuffix records that cmd is a namespace's `help` child.
func markHelpSuffix(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[helpSuffixAnnotation] = "true"
}

// findHelpSuffix returns cmd's `help` child, or nil when it has none. It
// matches on NAME rather than the annotation so that a namespace already
// carrying a `help` command — cobra's own, at the root — keeps the one it has
// instead of collecting a second.
func findHelpSuffix(cmd *cobra.Command) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == helpArgName {
			return sub
		}
	}
	return nil
}
