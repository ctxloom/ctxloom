package cli

import (
	"github.com/spf13/cobra"
)

// The deprecated `manage` alias namespaces, kept working while their real homes
// live elsewhere (CLI-primary reorg plan: `manage mcp install/uninstall` ->
// `mcp register/unregister`, `manage mcp servers *` -> `mcp server *`, `manage
// config *` -> top-level `config *`). A cobra command has exactly one parent, so
// these are distinct *cobra.Command values sharing the real homes' RunE bodies.
//
// They are separated from manage.go so the live harness-install namespace reads
// as itself: everything here exists only to keep an old spelling from breaking,
// and deleting the file is the whole of retiring them.

// manageMcpCmd is kept as a working alias namespace (CLI-primary reorg plan,
// Decision 3: `manage mcp install/uninstall` -> `mcp register/unregister`,
// `manage mcp servers *` -> `mcp server *`). Its leaves carry the real cobra
// Deprecated field; this parent stays undecorated (trustCmd's shape) so
// Deprecated doesn't hide the whole subtree from `--help`.
var manageMcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage ctxloom MCP registration and configured servers — moved under `ctxloom mcp`",
}

var manageMcpInstallCmd = &cobra.Command{
	Use:        "install",
	Short:      "Enable auto-registration of ctxloom's own MCP server",
	Deprecated: mcpRegisterDeprecation,
	Args:       cobra.NoArgs,
	RunE:       func(cmd *cobra.Command, _ []string) error { return setMcpAutoRegister(cmd, true) },
}

var manageMcpUninstallCmd = &cobra.Command{
	Use:        "uninstall",
	Short:      "Disable auto-registration of ctxloom's own MCP server",
	Deprecated: mcpUnregisterDeprecation,
	Args:       cobra.NoArgs,
	RunE:       func(cmd *cobra.Command, _ []string) error { return setMcpAutoRegister(cmd, false) },
}

var manageMcpServersCmd = &cobra.Command{
	Use:   "servers",
	Short: "List, add, remove, or show configured MCP servers",
}

// --- manage config (deprecated alias namespace) -----------------------------
//
// `manage config *` -> top-level `config *` (CLI-primary reorg plan, Decision
// 6). configCmd itself is now the top-level command (wired in config.go);
// these are distinct leaf commands sharing its RunE bodies — a cobra command
// has exactly one parent, so config.go's configCmd can't also hang off
// manageCmd. manageConfigCmd (the parent) stays undecorated, like trustCmd,
// so Deprecated on it wouldn't hide the leaves below from `--help`.
var manageConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or modify ctxloom configuration — moved to top-level `ctxloom config`",
	Long: `DEPRECATED: this command group moved to the top-level ` + "`ctxloom config`" + `.
Each subcommand below still runs and prints a one-line pointer to its new home.

Show or modify ctxloom configuration.`,
}

const (
	manageConfigShowDeprecation = "use `ctxloom config show` instead"
	manageConfigGetDeprecation  = "use `ctxloom config get` instead"
	manageConfigEditDeprecation = "use `ctxloom config edit` instead"
	manageConfigInitDeprecation = "use `ctxloom config init` instead"
)

var manageConfigShowCmd = &cobra.Command{
	Use:        "show",
	Short:      "Show full configuration",
	Deprecated: manageConfigShowDeprecation,
	RunE:       runConfigShow,
}

var manageConfigGetCmd = &cobra.Command{
	Use:        "get <section>",
	Short:      "Get a configuration section",
	Long:       configGetLong,
	Args:       cobra.ExactArgs(1),
	Deprecated: manageConfigGetDeprecation,
	RunE:       runConfigGet,
}

var manageConfigEditCmd = &cobra.Command{
	Use:        "edit",
	Short:      "Open config.yaml in $EDITOR",
	Args:       cobra.NoArgs,
	Deprecated: manageConfigEditDeprecation,
	RunE:       runConfigEdit,
}

var manageConfigInitCmd = &cobra.Command{
	Use:        "init",
	Short:      "Scaffold a default config.yaml (and remotes.yaml)",
	Long:       configInitLong,
	Args:       cobra.NoArgs,
	Deprecated: manageConfigInitDeprecation,
	RunE:       runConfigInit,
}

func init() {
	manageCmd.AddCommand(manageMcpCmd)
	manageMcpCmd.AddCommand(manageMcpInstallCmd)
	manageMcpCmd.AddCommand(manageMcpUninstallCmd)
	manageMcpCmd.AddCommand(manageMcpServersCmd)
	manageMcpServersCmd.AddCommand(mcpListCmd)
	manageMcpServersCmd.AddCommand(mcpAddCmd)
	manageMcpServersCmd.AddCommand(mcpRemoveCmd)
	manageMcpServersCmd.AddCommand(mcpShowCmd)

	manageCmd.AddCommand(manageConfigCmd)
	manageConfigCmd.AddCommand(manageConfigShowCmd)
	manageConfigCmd.AddCommand(manageConfigGetCmd)
	manageConfigCmd.AddCommand(manageConfigEditCmd)
	manageConfigCmd.AddCommand(manageConfigInitCmd)
	manageConfigInitCmd.Flags().StringVar(&configInitEngine, "engine", "claude-code", "AI engine to record in the scaffolded config")
}
