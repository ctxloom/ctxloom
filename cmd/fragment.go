package cmd

import (
	"github.com/spf13/cobra"
)

var fragmentCmd = &cobra.Command{
	Use:   "fragment",
	Short: "Manage context fragments",
	Long: `Manage context fragments - reusable context snippets for AI coding assistants.

Fragments are stored within bundle YAML files in .scm/bundles/ and are referenced
using the syntax: bundle#fragments/name

Examples:
  scm fragment list                              # List all fragments
  scm fragment show core#fragments/tdd           # Show fragment content
  scm fragment edit core#fragments/tdd           # Edit fragment content
  scm fragment create my-bundle coding-standards # Create new fragment
  scm fragment install scm-main/core           # Install bundle from remote`,
}

var fragmentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all fragments",
	Long: `List all fragments from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return listItems(ItemTypeFragment, fragmentListBundle)
	},
}

var fragmentListBundle string

var fragmentShowCmd = &cobra.Command{
	Use:   "show <bundle>#fragments/<name>",
	Short: "Show fragment content",
	Long: `Display the content of a specific fragment.

Reference format: bundle#fragments/name

Examples:
  scm fragment show core#fragments/tdd
  scm fragment show go-tools#fragments/testing`,
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
  scm fragment create my-bundle coding-standards
  scm fragment create go-tools testing-patterns`,
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
  scm fragment delete my-bundle#fragments/old-standard`,
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
  scm fragment edit core#fragments/tdd
  scm fragment edit go-tools#fragments/testing`,
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
  scm fragment distill core#fragments/tdd
  scm fragment distill go-tools#fragments/testing --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return distillItem(args[0], ItemTypeFragment, fragmentDistillForce)
	},
}

var fragmentDistillForce bool

var (
	fragmentInstallForce bool
	fragmentInstallBlind bool
)

var fragmentInstallCmd = &cobra.Command{
	Use:   "install <reference>",
	Short: "Install a bundle from remote",
	Long: `Install a bundle containing fragments from a remote repository.

This pulls the entire bundle (which contains fragments, prompts, etc.)
from the specified remote.

Reference formats:
  scm-main/core                    # Bundle from default remote path
  https://github.com/user/repo@v1/bundles/core   # Full URL

Examples:
  scm fragment install scm-main/core
  scm fragment install scm-main/go-development`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return installBundle(cmd, args[0], fragmentInstallForce, fragmentInstallBlind)
	},
}

var (
	fragmentPushPR      bool
	fragmentPushBranch  string
	fragmentPushMessage string
)

var fragmentPushCmd = &cobra.Command{
	Use:   "push <bundle> [remote]",
	Short: "Push a bundle to remote",
	Long: `Push a bundle containing fragments to a remote repository.

This publishes the entire bundle (which contains fragments, prompts, etc.)
to the specified remote.

Examples:
  scm fragment push my-bundle
  scm fragment push my-bundle scm-main
  scm fragment push my-bundle --pr`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		remoteName := ""
		if len(args) > 1 {
			remoteName = args[1]
		}
		return pushBundle(cmd, args[0], remoteName, fragmentPushPR, fragmentPushBranch, fragmentPushMessage)
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
	fragmentCmd.AddCommand(fragmentInstallCmd)
	fragmentCmd.AddCommand(fragmentPushCmd)

	fragmentListCmd.Flags().StringVarP(&fragmentListBundle, "bundle", "b", "", "Filter by bundle name")
	fragmentShowCmd.Flags().BoolVarP(&fragmentShowDistilled, "distilled", "d", false, "Show distilled version")
	fragmentDistillCmd.Flags().BoolVarP(&fragmentDistillForce, "force", "f", false, "Re-distill even if unchanged")

	fragmentInstallCmd.Flags().BoolVarP(&fragmentInstallForce, "force", "f", false, "Skip confirmation prompts")
	fragmentInstallCmd.Flags().BoolVar(&fragmentInstallBlind, "blind", false, "Skip security review display")

	fragmentPushCmd.Flags().BoolVar(&fragmentPushPR, "pr", false, "Create a pull request")
	fragmentPushCmd.Flags().StringVar(&fragmentPushBranch, "branch", "", "Target branch")
	fragmentPushCmd.Flags().StringVarP(&fragmentPushMessage, "message", "m", "", "Commit message")
}
