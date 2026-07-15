package cli

import (
	"github.com/spf13/cobra"
)

var commandCmd = &cobra.Command{
	Use:   "command",
	Short: "Manage commands",
	Long: `Manage commands - reusable prompt/command templates for AI coding assistants.

Commands live inside bundles — local bundle YAML files in .ctxloom/content/bundles/
or lockfile-pinned remote bundles — and are referenced using the syntax:
bundle#commands/name

Examples:
  ctxloom command list                                 # List all commands
  ctxloom command show core#commands/code-review        # Show command content
  ctxloom command edit core#commands/code-review        # Edit command content
  ctxloom command create my-bundle code-review          # Create new command`,
}

var commandListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all commands",
	Long: `List all commands from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return listItems(cmd, ItemTypeCommand, commandListBundle)
	},
}

var commandListBundle string

var commandShowCmd = &cobra.Command{
	Use:   "show <bundle>#commands/<name>",
	Short: "Show command content",
	Long: `Display the content of a specific command.

Reference format: bundle#commands/name

Examples:
  ctxloom command show core#commands/code-review
  ctxloom command show go-tools#commands/testing`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return showItem(cmd, args[0], ItemTypeCommand, commandShowDistilled, commandShowInteractive)
	},
}

var (
	commandShowDistilled   bool
	commandShowInteractive bool
)

var commandCreateCmd = &cobra.Command{
	Use:   "create <bundle> <name>",
	Short: "Create a new command",
	Long: `Create a new command in an existing bundle.

The command will be created with placeholder content that you can edit.

Examples:
  ctxloom command create my-bundle code-review
  ctxloom command create go-tools testing-patterns`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return createItem(args[0], args[1], ItemTypeCommand)
	},
}

var commandDeleteCmd = &cobra.Command{
	Use:   "delete <bundle>#commands/<name>",
	Short: "Delete a command",
	Long: `Delete a command from a bundle.

Reference format: bundle#commands/name

Examples:
  ctxloom command delete my-bundle#commands/old-command`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return deleteItem(args[0], ItemTypeCommand)
	},
}

var commandEditCmd = &cobra.Command{
	Use:   "edit <bundle>#commands/<name>",
	Short: "Edit a command",
	Long: `Edit a command's content using your configured editor.

Reference format: bundle#commands/name

After editing, the command will be automatically re-distilled unless marked as no_distill.

Examples:
  ctxloom command edit core#commands/code-review
  ctxloom command edit go-tools#commands/testing`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editItem(args[0], ItemTypeCommand)
	},
}

var commandDistillCmd = &cobra.Command{
	Use:   "distill <bundle>#commands/<name>",
	Short: "Distill a command",
	Long: `Distill a command to create a token-efficient version.

Reference format: bundle#commands/name

Examples:
  ctxloom command distill core#commands/code-review
  ctxloom command distill go-tools#commands/testing --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return distillItem(args[0], ItemTypeCommand, commandDistillForce)
	},
}

var commandDistillForce bool

var (
	commandPushPR      bool
	commandPushMessage string
	commandPushSign    bool
	commandPushNoSign  bool
)

var commandPushCmd = &cobra.Command{
	Use:   "push <bundle> [remote]",
	Short: "Push a bundle to remote",
	Long: `Push a bundle containing commands to a remote repository.

This publishes the entire bundle (which contains commands, fragments, etc.)
to the specified remote.

--sign publishes a detached signature alongside the bundle (signature-
envelope spec §3.1) so anyone who trusts your key can verify it came from
you, using the same zero-config key discovery 'ctxloom sign' uses. Set
'sign.default: true' in config to make every push sign unless --no-sign is
given for one invocation.

Examples:
  ctxloom command push my-bundle
  ctxloom command push my-bundle ctxloom-default
  ctxloom command push my-bundle --pr
  ctxloom command push my-bundle --sign`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		remoteName := ""
		if len(args) > 1 {
			remoteName = args[1]
		}
		return pushBundle(cmd, args[0], remoteName, commandPushPR, commandPushMessage, commandPushSign, commandPushNoSign)
	},
}

func init() {
	rootCmd.AddCommand(commandCmd)

	commandCmd.AddCommand(commandListCmd)
	commandCmd.AddCommand(commandShowCmd)
	commandCmd.AddCommand(commandCreateCmd)
	commandCmd.AddCommand(commandDeleteCmd)
	commandCmd.AddCommand(commandEditCmd)
	commandCmd.AddCommand(commandDistillCmd)
	commandCmd.AddCommand(commandPushCmd)

	commandListCmd.Flags().StringVarP(&commandListBundle, "bundle", "b", "", "Filter by bundle name")
	commandShowCmd.Flags().BoolVarP(&commandShowDistilled, "distilled", "d", false, "Show distilled version")
	commandShowCmd.Flags().BoolVarP(&commandShowInteractive, "interactive", "i", false, "Review effective trust and offer to trust/blacklist (interactive terminal only)")
	commandDistillCmd.Flags().BoolVarP(&commandDistillForce, "force", "f", false, "Re-distill even if unchanged")

	commandPushCmd.Flags().BoolVar(&commandPushPR, "pr", false, "Create a pull request")
	commandPushCmd.Flags().StringVarP(&commandPushMessage, "message", "m", "", "Commit message")
	commandPushCmd.Flags().BoolVar(&commandPushSign, "sign", false, "sign the published bundle (spec §3.1)")
	commandPushCmd.Flags().BoolVar(&commandPushNoSign, "no-sign", false, "don't sign, even if sign.default is true")
}
