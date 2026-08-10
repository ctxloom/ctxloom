package cli

import (
	"github.com/spf13/cobra"
)

// Bare `ctxloom command` lists the commands: the collection is the one
// thing the noun is about, and reading it touches nothing.
var commandCmd = groupNodeDefault(&cobra.Command{
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
  ctxloom command create my-bundle code-review          # Create new command
  ctxloom command remove my-bundle#commands/old-one --yes # Remove a command`,
}, "list")

var commandListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all commands",
	Long: `List all commands from all installed bundles.

Use --bundle to filter by a specific bundle.`,
	RunE: runCommandList,
}

func runCommandList(cmd *cobra.Command, args []string) error {
	return listItems(cmd, ItemTypeCommand, commandListBundle)
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
	RunE: runCommandShow,
}

func runCommandShow(cmd *cobra.Command, args []string) error {
	return showItem(cmd, args[0], ItemTypeCommand, commandShowDistilled, commandShowInteractive)
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
	RunE: runCommandCreate,
}

func runCommandCreate(cmd *cobra.Command, args []string) error {
	return createItem(cmd, args[0], args[1], ItemTypeCommand)
}

var commandRemoveYes bool

var commandRemoveCmd = &cobra.Command{
	Use:     "remove <bundle>#commands/<name>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove a command",
	Long: `Remove a command from a bundle.

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it.

Reference format: bundle#commands/name

Examples:
  ctxloom command remove my-bundle#commands/old-command
  ctxloom command remove my-bundle#commands/old-command --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runCommandRemove,
}

func runCommandRemove(cmd *cobra.Command, args []string) error {
	return removeItem(cmd, args[0], ItemTypeCommand, commandRemoveYes)
}

var commandEditCmd = &cobra.Command{
	Use:   "edit <bundle>#commands/<name>",
	Short: "Edit a command",
	Long: `Edit a command's content using your configured editor.

Reference format: bundle#commands/name

After editing, the command will be automatically re-distilled unless marked as
no_distill. Use --no-distill to skip re-distillation for just this edit (e.g. a
typo fix) without burning an LLM call — the distilled form is left empty
(never stale) until you run 'ctxloom command distill'.

Examples:
  ctxloom command edit core#commands/code-review
  ctxloom command edit go-tools#commands/testing
  ctxloom command edit core#commands/code-review --no-distill`,
	Args: cobra.ExactArgs(1),
	RunE: runCommandEdit,
}

func runCommandEdit(cmd *cobra.Command, args []string) error {
	return editItem(cmd, args[0], ItemTypeCommand, commandEditNoDistill)
}

var commandEditNoDistill bool

var commandDistillCmd = &cobra.Command{
	Use:   "distill <bundle>#commands/<name>",
	Short: "Distill a command",
	Long: `Distill a command to create a token-efficient version.

Reference format: bundle#commands/name

Examples:
  ctxloom command distill core#commands/code-review
  ctxloom command distill go-tools#commands/testing --force`,
	Args: cobra.ExactArgs(1),
	RunE: runCommandDistill,
}

func runCommandDistill(cmd *cobra.Command, args []string) error {
	return distillItem(cmd, args[0], ItemTypeCommand, commandDistillForce)
}

var commandDistillForce bool

func init() {
	rootCmd.AddCommand(commandCmd)

	commandCmd.AddCommand(commandListCmd)
	commandCmd.AddCommand(commandShowCmd)
	commandCmd.AddCommand(commandCreateCmd)
	commandCmd.AddCommand(commandRemoveCmd)
	commandCmd.AddCommand(commandEditCmd)
	commandCmd.AddCommand(commandDistillCmd)

	commandListCmd.Flags().StringVarP(&commandListBundle, "bundle", "b", "", "Filter by bundle name")
	commandShowCmd.Flags().BoolVarP(&commandShowDistilled, "distilled", "d", false, "Show distilled version")
	commandShowCmd.Flags().BoolVarP(&commandShowInteractive, "interactive", "i", false, "Review effective trust and offer to trust/blacklist (interactive terminal only)")
	commandEditCmd.Flags().BoolVar(&commandEditNoDistill, "no-distill", false, "Skip re-distillation for this edit (leaves the distilled form empty, never stale)")
	commandDistillCmd.Flags().BoolVarP(&commandDistillForce, "force", "f", false, "Re-distill even if unchanged")
	commandRemoveCmd.Flags().BoolVarP(&commandRemoveYes, "yes", "y", false, "Apply the removal this invocation would report (default: report only)")
}
