package cmd

import (
	"github.com/spf13/cobra"
)

var fragmentCmd = &cobra.Command{
	Use:   "fragment",
	Short: "Manage context fragments",
	Long: `Manage context fragments - reusable context snippets for AI coding assistants.

Fragments are stored within bundle YAML files in .ctxloom/bundles/ and are referenced
using the syntax: bundle#fragments/name

Examples:
  ctxloom fragment list                              # List all fragments
  ctxloom fragment show core#fragments/tdd           # Show fragment content
  ctxloom fragment edit core#fragments/tdd           # Edit fragment content
  ctxloom fragment create my-bundle coding-standards # Create new fragment`,
}

var fragmentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all fragments",
	Long: `List all fragments from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return listItems(cmd, ItemTypeFragment, fragmentListBundle)
	},
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return showItem(args[0], ItemTypeFragment, fragmentShowDistilled)
	},
}

var fragmentShowDistilled bool

var fragmentCreateCmd = &cobra.Command{
	Use:   "create <bundle> <name>",
	Short: "Create a new fragment",
	Long: `Create a new fragment in an existing bundle.

The fragment will be created with placeholder content that you can edit.

Examples:
  ctxloom fragment create my-bundle coding-standards
  ctxloom fragment create go-tools testing-patterns`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return createItem(args[0], args[1], ItemTypeFragment)
	},
}

var fragmentDeleteCmd = &cobra.Command{
	Use:   "delete <bundle>#fragments/<name>",
	Short: "Delete a fragment",
	Long: `Delete a fragment from a bundle.

Reference format: bundle#fragments/name

Examples:
  ctxloom fragment delete my-bundle#fragments/old-standard`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return deleteItem(args[0], ItemTypeFragment)
	},
}

var fragmentEditCmd = &cobra.Command{
	Use:   "edit <bundle>#fragments/<name>",
	Short: "Edit a fragment",
	Long: `Edit a fragment's content using your configured editor.

Reference format: bundle#fragments/name

After editing, the fragment will be automatically re-distilled unless marked as no_distill.

Examples:
  ctxloom fragment edit core#fragments/tdd
  ctxloom fragment edit go-tools#fragments/testing`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editItem(args[0], ItemTypeFragment)
	},
}

var fragmentDistillCmd = &cobra.Command{
	Use:   "distill <bundle>#fragments/<name>",
	Short: "Distill a fragment",
	Long: `Distill a fragment to create a token-efficient version.

Reference format: bundle#fragments/name

Examples:
  ctxloom fragment distill core#fragments/tdd
  ctxloom fragment distill go-tools#fragments/testing --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return distillItem(args[0], ItemTypeFragment, fragmentDistillForce)
	},
}

var fragmentDistillForce bool

var fragmentSearchTags []string

var fragmentSearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search fragments",
	Long: `Search for fragments by name or tags.

Examples:
  ctxloom fragment search cache           # Search by name
  ctxloom fragment search -t golang       # Search by tag
  ctxloom fragment search -t golang,testing  # Search by multiple tags`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = args[0]
		}
		// Use unified search with local-only scope for fragments
		return runUnifiedSearch(cmd.Context(), query, fragmentSearchTags, "fragment", true, false)
	},
}

var (
	fragmentPushPR      bool
	fragmentPushMessage string
)

var fragmentPushCmd = &cobra.Command{
	Use:   "push <bundle> [remote]",
	Short: "Push a bundle to remote",
	Long: `Push a bundle containing fragments to a remote repository.

This publishes the entire bundle (which contains fragments, prompts, etc.)
to the specified remote.

Examples:
  ctxloom fragment push my-bundle
  ctxloom fragment push my-bundle ctxloom-default
  ctxloom fragment push my-bundle --pr`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		remoteName := ""
		if len(args) > 1 {
			remoteName = args[1]
		}
		return pushBundle(cmd, args[0], remoteName, fragmentPushPR, fragmentPushMessage)
	},
}

func init() {
	rootCmd.AddCommand(fragmentCmd)

	fragmentCmd.AddCommand(fragmentListCmd)
	fragmentCmd.AddCommand(fragmentShowCmd)
	fragmentCmd.AddCommand(fragmentCreateCmd)
	fragmentCmd.AddCommand(fragmentDeleteCmd)
	fragmentCmd.AddCommand(fragmentEditCmd)
	fragmentCmd.AddCommand(fragmentDistillCmd)
	fragmentCmd.AddCommand(fragmentPushCmd)
	fragmentCmd.AddCommand(fragmentSearchCmd)

	fragmentListCmd.Flags().StringVarP(&fragmentListBundle, "bundle", "b", "", "Filter by bundle name")
	fragmentShowCmd.Flags().BoolVarP(&fragmentShowDistilled, "distilled", "d", false, "Show distilled version")
	fragmentDistillCmd.Flags().BoolVarP(&fragmentDistillForce, "force", "f", false, "Re-distill even if unchanged")

	fragmentPushCmd.Flags().BoolVar(&fragmentPushPR, "pr", false, "Create a pull request")
	fragmentPushCmd.Flags().StringVarP(&fragmentPushMessage, "message", "m", "", "Commit message")

	fragmentSearchCmd.Flags().StringSliceVarP(&fragmentSearchTags, "tag", "t", nil, "Filter by tags (comma-separated)")
}
