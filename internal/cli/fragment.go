package cli

import (
	"github.com/spf13/cobra"
)

var fragmentCmd = groupNode(&cobra.Command{
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
  ctxloom fragment create my-bundle coding-standards # Create new fragment`,
})

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

var fragmentDeleteCmd = &cobra.Command{
	Use:   "delete <bundle>#fragments/<name>",
	Short: "Delete a fragment",
	Long: `Delete a fragment from a bundle.

Reference format: bundle#fragments/name

Examples:
  ctxloom fragment delete my-bundle#fragments/old-standard`,
	Args: cobra.ExactArgs(1),
	RunE: runFragmentDelete,
}

func runFragmentDelete(cmd *cobra.Command, args []string) error {
	return deleteItem(cmd, args[0], ItemTypeFragment)
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
	fragmentCmd.AddCommand(fragmentDeleteCmd)
	fragmentCmd.AddCommand(fragmentEditCmd)
	fragmentCmd.AddCommand(fragmentDistillCmd)

	fragmentListCmd.Flags().StringVarP(&fragmentListBundle, "bundle", "b", "", "Filter by bundle name")
	fragmentShowCmd.Flags().BoolVarP(&fragmentShowDistilled, "distilled", "d", false, "Show distilled version")
	fragmentShowCmd.Flags().BoolVarP(&fragmentShowInteractive, "interactive", "i", false, "Review effective trust and offer to trust/blacklist (interactive terminal only)")
	fragmentEditCmd.Flags().BoolVar(&fragmentEditNoDistill, "no-distill", false, "Skip re-distillation for this edit (leaves the distilled form empty, never stale)")
	fragmentDistillCmd.Flags().BoolVarP(&fragmentDistillForce, "force", "f", false, "Re-distill even if unchanged")
}
