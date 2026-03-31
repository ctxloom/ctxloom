package cmd

import (
	"github.com/spf13/cobra"
)

var promptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Manage prompts",
	Long: `Manage prompts - reusable prompt templates for AI coding assistants.

Prompts are stored within bundle YAML files in .ctxloom/bundles/ and are referenced
using the syntax: bundle#prompts/name

Examples:
  ctxloom prompt list                              # List all prompts
  ctxloom prompt show core#prompts/code-review     # Show prompt content
  ctxloom prompt edit core#prompts/code-review     # Edit prompt content
  ctxloom prompt create my-bundle code-review      # Create new prompt
  ctxloom prompt install scm-main/core           # Install bundle from remote`,
}

var promptListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all prompts",
	Long: `List all prompts from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return listItems(ItemTypePrompt, promptListBundle)
	},
}

var promptListBundle string

var promptShowCmd = &cobra.Command{
	Use:   "show <bundle>#prompts/<name>",
	Short: "Show prompt content",
	Long: `Display the content of a specific prompt.

Reference format: bundle#prompts/name

Examples:
  ctxloom prompt show core#prompts/code-review
  ctxloom prompt show go-tools#prompts/testing`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return showItem(args[0], ItemTypePrompt, promptShowDistilled)
	},
}

var promptShowDistilled bool

var promptCreateCmd = &cobra.Command{
	Use:   "create <bundle> <name>",
	Short: "Create a new prompt",
	Long: `Create a new prompt in an existing bundle.

The prompt will be created with placeholder content that you can edit.

Examples:
  ctxloom prompt create my-bundle code-review
  ctxloom prompt create go-tools testing-patterns`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return createItem(args[0], args[1], ItemTypePrompt)
	},
}

var promptDeleteCmd = &cobra.Command{
	Use:   "delete <bundle>#prompts/<name>",
	Short: "Delete a prompt",
	Long: `Delete a prompt from a bundle.

Reference format: bundle#prompts/name

Examples:
  ctxloom prompt delete my-bundle#prompts/old-prompt`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return deleteItem(args[0], ItemTypePrompt)
	},
}

var promptEditCmd = &cobra.Command{
	Use:   "edit <bundle>#prompts/<name>",
	Short: "Edit a prompt",
	Long: `Edit a prompt's content using your configured editor.

Reference format: bundle#prompts/name

After editing, the prompt will be automatically re-distilled unless marked as no_distill.

Examples:
  ctxloom prompt edit core#prompts/code-review
  ctxloom prompt edit go-tools#prompts/testing`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editItem(args[0], ItemTypePrompt)
	},
}

var promptDistillCmd = &cobra.Command{
	Use:   "distill <bundle>#prompts/<name>",
	Short: "Distill a prompt",
	Long: `Distill a prompt to create a token-efficient version.

Reference format: bundle#prompts/name

Examples:
  ctxloom prompt distill core#prompts/code-review
  ctxloom prompt distill go-tools#prompts/testing --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return distillItem(args[0], ItemTypePrompt, promptDistillForce)
	},
}

var promptDistillForce bool

var (
	promptInstallForce bool
	promptInstallBlind bool
)

var promptInstallCmd = &cobra.Command{
	Use:   "install <reference>",
	Short: "Install a bundle from remote",
	Long: `Install a bundle containing prompts from a remote repository.

This pulls the entire bundle (which contains prompts, fragments, etc.)
from the specified remote.

Reference formats:
  scm-main/core                    # Bundle from default remote path
  https://github.com/user/repo@v1/bundles/core   # Full URL

Examples:
  ctxloom prompt install scm-main/core
  ctxloom prompt install scm-main/go-development`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return installBundle(cmd, args[0], promptInstallForce, promptInstallBlind)
	},
}

var (
	promptPushPR      bool
	promptPushBranch  string
	promptPushMessage string
)

var promptPushCmd = &cobra.Command{
	Use:   "push <bundle> [remote]",
	Short: "Push a bundle to remote",
	Long: `Push a bundle containing prompts to a remote repository.

This publishes the entire bundle (which contains prompts, fragments, etc.)
to the specified remote.

Examples:
  ctxloom prompt push my-bundle
  ctxloom prompt push my-bundle scm-main
  ctxloom prompt push my-bundle --pr`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		remoteName := ""
		if len(args) > 1 {
			remoteName = args[1]
		}
		return pushBundle(cmd, args[0], remoteName, promptPushPR, promptPushBranch, promptPushMessage)
	},
}

func init() {
	rootCmd.AddCommand(promptCmd)

	promptCmd.AddCommand(promptListCmd)
	promptCmd.AddCommand(promptShowCmd)
	promptCmd.AddCommand(promptCreateCmd)
	promptCmd.AddCommand(promptDeleteCmd)
	promptCmd.AddCommand(promptEditCmd)
	promptCmd.AddCommand(promptDistillCmd)
	promptCmd.AddCommand(promptInstallCmd)
	promptCmd.AddCommand(promptPushCmd)

	promptListCmd.Flags().StringVarP(&promptListBundle, "bundle", "b", "", "Filter by bundle name")
	promptShowCmd.Flags().BoolVarP(&promptShowDistilled, "distilled", "d", false, "Show distilled version")
	promptDistillCmd.Flags().BoolVarP(&promptDistillForce, "force", "f", false, "Re-distill even if unchanged")

	promptInstallCmd.Flags().BoolVarP(&promptInstallForce, "force", "f", false, "Skip confirmation prompts")
	promptInstallCmd.Flags().BoolVar(&promptInstallBlind, "blind", false, "Skip security review display")

	promptPushCmd.Flags().BoolVar(&promptPushPR, "pr", false, "Create a pull request")
	promptPushCmd.Flags().StringVar(&promptPushBranch, "branch", "", "Target branch")
	promptPushCmd.Flags().StringVarP(&promptPushMessage, "message", "m", "", "Commit message")
}
