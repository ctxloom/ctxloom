package cmd

import (
	"github.com/spf13/cobra"
)

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Manage skills",
	Long: `Manage skills - reusable prompt/skill templates for AI coding assistants.

Skills are stored within bundle YAML files in .ctxloom/bundles/ and are referenced
using the syntax: bundle#skills/name

Examples:
  ctxloom skill list                              # List all skills
  ctxloom skill show core#skills/code-review      # Show skill content
  ctxloom skill edit core#skills/code-review      # Edit skill content
  ctxloom skill create my-bundle code-review      # Create new skill`,
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all skills",
	Long: `List all skills from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return listItems(cmd, ItemTypeSkill, skillListBundle)
	},
}

var skillListBundle string

var skillShowCmd = &cobra.Command{
	Use:   "show <bundle>#skills/<name>",
	Short: "Show skill content",
	Long: `Display the content of a specific skill.

Reference format: bundle#skills/name

Examples:
  ctxloom skill show core#skills/code-review
  ctxloom skill show go-tools#skills/testing`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return showItem(cmd, args[0], ItemTypeSkill, skillShowDistilled, skillShowInteractive)
	},
}

var (
	skillShowDistilled   bool
	skillShowInteractive bool
)

var skillCreateCmd = &cobra.Command{
	Use:   "create <bundle> <name>",
	Short: "Create a new skill",
	Long: `Create a new skill in an existing bundle.

The skill will be created with placeholder content that you can edit.

Examples:
  ctxloom skill create my-bundle code-review
  ctxloom skill create go-tools testing-patterns`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return createItem(args[0], args[1], ItemTypeSkill)
	},
}

var skillDeleteCmd = &cobra.Command{
	Use:   "delete <bundle>#skills/<name>",
	Short: "Delete a skill",
	Long: `Delete a skill from a bundle.

Reference format: bundle#skills/name

Examples:
  ctxloom skill delete my-bundle#skills/old-skill`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return deleteItem(args[0], ItemTypeSkill)
	},
}

var skillEditCmd = &cobra.Command{
	Use:   "edit <bundle>#skills/<name>",
	Short: "Edit a skill",
	Long: `Edit a skill's content using your configured editor.

Reference format: bundle#skills/name

After editing, the skill will be automatically re-distilled unless marked as no_distill.

Examples:
  ctxloom skill edit core#skills/code-review
  ctxloom skill edit go-tools#skills/testing`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editItem(args[0], ItemTypeSkill)
	},
}

var skillDistillCmd = &cobra.Command{
	Use:   "distill <bundle>#skills/<name>",
	Short: "Distill a skill",
	Long: `Distill a skill to create a token-efficient version.

Reference format: bundle#skills/name

Examples:
  ctxloom skill distill core#skills/code-review
  ctxloom skill distill go-tools#skills/testing --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return distillItem(args[0], ItemTypeSkill, skillDistillForce)
	},
}

var skillDistillForce bool

var (
	skillPushPR      bool
	skillPushMessage string
)

var skillPushCmd = &cobra.Command{
	Use:   "push <bundle> [remote]",
	Short: "Push a bundle to remote",
	Long: `Push a bundle containing skills to a remote repository.

This publishes the entire bundle (which contains skills, fragments, etc.)
to the specified remote.

Examples:
  ctxloom skill push my-bundle
  ctxloom skill push my-bundle ctxloom-default
  ctxloom skill push my-bundle --pr`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		remoteName := ""
		if len(args) > 1 {
			remoteName = args[1]
		}
		return pushBundle(cmd, args[0], remoteName, skillPushPR, skillPushMessage)
	},
}

func init() {
	rootCmd.AddCommand(skillCmd)

	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillShowCmd)
	skillCmd.AddCommand(skillCreateCmd)
	skillCmd.AddCommand(skillDeleteCmd)
	skillCmd.AddCommand(skillEditCmd)
	skillCmd.AddCommand(skillDistillCmd)
	skillCmd.AddCommand(skillPushCmd)

	skillListCmd.Flags().StringVarP(&skillListBundle, "bundle", "b", "", "Filter by bundle name")
	skillShowCmd.Flags().BoolVarP(&skillShowDistilled, "distilled", "d", false, "Show distilled version")
	skillShowCmd.Flags().BoolVarP(&skillShowInteractive, "interactive", "i", false, "Review effective trust and offer to trust/blacklist (interactive terminal only)")
	skillDistillCmd.Flags().BoolVarP(&skillDistillForce, "force", "f", false, "Re-distill even if unchanged")

	skillPushCmd.Flags().BoolVar(&skillPushPR, "pr", false, "Create a pull request")
	skillPushCmd.Flags().StringVarP(&skillPushMessage, "message", "m", "", "Commit message")
}
