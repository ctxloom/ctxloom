package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// Bare `ctxloom fragment` lists the fragments: the collection is the one
// thing the noun is about, and reading it touches nothing.
var fragmentCmd = groupNodeDefault(&cobra.Command{
	Use:   "fragment",
	Short: "Manage context fragments",
	Long: `Manage context fragments - reusable context snippets for AI coding assistants.

Fragments live inside bundles — local bundle YAML files in .ctxloom/content/bundles/
or lockfile-pinned remote bundles — and are referenced using the syntax:
bundle#fragments/name

Examples:
  ctxloom fragment list                              # List all fragments
  ctxloom fragment show core#fragments/tdd           # Show fragment content
  ctxloom fragment edit core#fragments/tdd           # Edit fragment content
  ctxloom fragment create my-bundle coding-standards # Create new fragment
  ctxloom fragment remove my-bundle#fragments/old-one --yes # Remove a fragment`,
}, "list")

var fragmentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all fragments",
	Long: `List all fragments from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: runFragmentList,
}

func runFragmentList(cmd *cobra.Command, args []string) error {
	return listItems(cmd, ItemTypeFragment, fragmentListBundle)
}

var fragmentListBundle string

var fragmentShowCmd = &cobra.Command{
	Use:   "show <bundle>#fragments/<name>",
	Short: "Show fragment content",
	Long: `Display the content of a specific fragment.

Reference format: bundle#fragments/name

Examples:
  ctxloom fragment show core#fragments/tdd
  ctxloom fragment show go-tools#fragments/testing`,
	Args: cobra.ExactArgs(1),
	RunE: runFragmentShow,
}

func runFragmentShow(cmd *cobra.Command, args []string) error {
	return showItem(cmd, args[0], ItemTypeFragment, fragmentShowDistilled, fragmentShowInteractive)
}

var (
	fragmentShowDistilled   bool
	fragmentShowInteractive bool
)

var fragmentCreateCmd = &cobra.Command{
	Use:   "create <bundle> <name>",
	Short: "Create a new fragment",
	Long: `Create a new fragment in an existing bundle.

The fragment will be created with placeholder content that you can edit.

Examples:
  ctxloom fragment create my-bundle coding-standards
  ctxloom fragment create go-tools testing-patterns`,
	Args: cobra.ExactArgs(2),
	RunE: runFragmentCreate,
}

func runFragmentCreate(cmd *cobra.Command, args []string) error {
	return createItem(cmd, args[0], args[1], ItemTypeFragment)
}

var fragmentRemoveYes bool

var fragmentRemoveCmd = &cobra.Command{
	Use:     "remove <bundle>#fragments/<name>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove a fragment",
	Long: `Remove a fragment from a bundle.

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it.

Reference format: bundle#fragments/name

Examples:
  ctxloom fragment remove my-bundle#fragments/old-standard
  ctxloom fragment remove my-bundle#fragments/old-standard --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runFragmentRemove,
}

func runFragmentRemove(cmd *cobra.Command, args []string) error {
	return removeItem(cmd, args[0], ItemTypeFragment, fragmentRemoveYes)
}

var fragmentEditCmd = &cobra.Command{
	Use:   "edit <bundle>#fragments/<name>",
	Short: "Edit a fragment",
	Long: `Edit a fragment's content using your configured editor.

Reference format: bundle#fragments/name

After editing, the fragment will be automatically re-distilled unless marked as
no_distill. Use --no-distill to skip re-distillation for just this edit (e.g. a
typo fix) without burning an LLM call — the distilled form is left empty
(never stale) until you run 'ctxloom fragment distill'.

Examples:
  ctxloom fragment edit core#fragments/tdd
  ctxloom fragment edit go-tools#fragments/testing
  ctxloom fragment edit core#fragments/tdd --no-distill`,
	Args: cobra.ExactArgs(1),
	RunE: runFragmentEdit,
}

func runFragmentEdit(cmd *cobra.Command, args []string) error {
	return editItem(cmd, args[0], ItemTypeFragment, fragmentEditNoDistill)
}

var fragmentEditNoDistill bool

var fragmentDistillCmd = &cobra.Command{
	Use:   "distill <bundle>#fragments/<name>",
	Short: "Distill a fragment",
	Long: `Distill a fragment to create a token-efficient version.

Reference format: bundle#fragments/name

Examples:
  ctxloom fragment distill core#fragments/tdd
  ctxloom fragment distill go-tools#fragments/testing --force`,
	Args: cobra.ExactArgs(1),
	RunE: runFragmentDistill,
}

func runFragmentDistill(cmd *cobra.Command, args []string) error {
	return distillItem(cmd, args[0], ItemTypeFragment, fragmentDistillForce)
}

var fragmentDistillForce bool

func init() {
	rootCmd.AddCommand(fragmentCmd)

	fragmentCmd.AddCommand(fragmentListCmd)
	fragmentCmd.AddCommand(fragmentShowCmd)
	fragmentCmd.AddCommand(fragmentCreateCmd)
	fragmentCmd.AddCommand(fragmentRemoveCmd)
	fragmentCmd.AddCommand(fragmentEditCmd)
	fragmentCmd.AddCommand(fragmentDistillCmd)
	fragmentCmd.AddCommand(fragmentPremisesCmd)

	fragmentListCmd.Flags().StringVarP(&fragmentListBundle, "bundle", "b", "", "Filter by bundle name")
	fragmentShowCmd.Flags().BoolVarP(&fragmentShowDistilled, "distilled", "d", false, "Show distilled version")
	fragmentShowCmd.Flags().BoolVarP(&fragmentShowInteractive, "interactive", "i", false, "Review effective trust and offer to trust/blacklist (interactive terminal only)")
	fragmentEditCmd.Flags().BoolVar(&fragmentEditNoDistill, "no-distill", false, "Skip re-distillation for this edit (leaves the distilled form empty, never stale)")
	fragmentDistillCmd.Flags().BoolVarP(&fragmentDistillForce, "force", "f", false, "Re-distill even if unchanged")
	fragmentRemoveCmd.Flags().BoolVarP(&fragmentRemoveYes, "yes", "y", false, "Apply the removal this invocation would report (default: report only)")
}

// The premise index is PULLED by the agent, not pushed into its context, and
// that is the point rather than a limitation. Assembled context is delivered
// once at launch into the session's system prompt, and in-process subagents
// inherit it wholesale -- there is no per-agent scoping, so a pushed index
// cannot be tailored and cannot reach a child that ctxloom never mediated. A
// command any agent can run needs none of that: it asks when it has a moment to
// match, which is also the only time the answer means anything.
var fragmentPremisesCmd = &cobra.Command{
	Use:   "premises",
	Short: "List conditionally-loaded fragments and the premise each applies under",
	Long: `List every fragment that carries a premise, with the condition under which it applies.

A premised fragment is NOT loaded unconditionally. This command is how an agent
asks what is available: it prints each fragment's qualified reference and its
premise, plus the instruction for deciding between them. Fragments carrying no
premise are absent -- they are always loaded, so there is nothing to decide.

Having chosen, load one with its reference:

  ctxloom fragment premises                       # what is on offer, and when it applies
  ctxloom fragment show core#fragments/tdd        # load one you selected`,
	Args: cobra.NoArgs,
	RunE: runFragmentPremises,
}

func runFragmentPremises(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	entries, err := operations.PremiseIndex(cfg.BundleLoader().Catalog())
	if err != nil {
		return fmt.Errorf("failed to list premised fragments: %w", err)
	}
	cmd.Println(renderPremiseListing(entries))
	return nil
}

// renderPremiseListing is the command's whole decision, extracted so a test can
// exercise THIS function rather than a copy of it. A test that reimplements the
// branch it is checking passes whatever the command later does.
//
// An empty index is REPORTED, never rendered as an empty instruction: a corpus
// where nothing is conditional is a valid state, and an agent told to "select
// from the following" with nothing following is being asked to choose from an
// empty set.
func renderPremiseListing(entries []operations.PremiseIndexEntry) string {
	if len(entries) == 0 {
		return "No fragments carry a premise: every fragment in this project's corpus is loaded unconditionally."
	}
	return operations.RenderPremiseIndex(entries)
}
