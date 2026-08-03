package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run ctxloom as an MCP server",
	Long: `Run ctxloom as an MCP (Model Context Protocol) server over stdio.

When called without subcommands, runs ctxloom as an MCP server over stdio.

RUNNING AS MCP SERVER:
  ctxloom mcp              Run as MCP server over stdio
  ctxloom mcp serve        Alias for running as MCP server

  Available tools when running as server:
    Context:  assemble_context, search_content, search_library
    Sessions: compact_session, list_sessions, load_session, get_previous_session, recover_session
    Agents:   agent_run, agent_send, agent_recv (delegated child sessions +
              the in-memory coordinator/executor message bus)

  Read-only listings (fragments, profiles, prompts, remotes, mcp-servers,
  sessions) are exposed as MCP resources (ctxloom://...), not tools. All
  management (bundles, remotes, review/approve, trust, pinning) is done with
  the ctxloom CLI, not MCP tools. Task tracking moved to the standalone
  taskloom binary; its MCP server ('taskloom mcp') serves the task_* tools.

Manage configured MCP servers under 'ctxloom mcp server' and ctxloom's own
auto-registration under 'ctxloom mcp register'/'ctxloom mcp unregister'.`,
	// NoArgs: without it, a stale invocation like `ctxloom mcp list` would
	// silently start a stdio MCP server that sits waiting on stdin.
	Args: cobra.NoArgs,
	RunE: runMCPServerSDK,
}

// MCP subcommands for managing MCP server configurations

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run as MCP server over stdio",
	Long:  `Run ctxloom as an MCP (Model Context Protocol) server over stdio. This is the default behavior when running 'ctxloom mcp' without subcommands.`,
	// NoArgs for the same reason as its bare `ctxloom mcp` twin above: this
	// RunE is the stdio server, so a stale invocation like `ctxloom mcp serve
	// list` would otherwise sit waiting on stdin instead of reporting the
	// mistake.
	Args: cobra.NoArgs,
	RunE: runMCPServerSDK,
}

// mcpServerListCmd is the canonical spine's `list` for the MCP-server noun.
var mcpServerListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured MCP servers",
	RunE:    runMCPList,
}

// mcpListRow is the --format json shape for `ctxloom mcp list`: a configured MCP
// server plus its effective-trust stamp. These are project/plugin-level (local)
// servers; per the trust model they are first-party (the user configured them
// in this project), so they are exposed via the local exemption unless
// explicitly rejected. (Bundle-sourced MCP items — addressed
// <bundle>#mcp/<name> — are gated at their own choke and are not what this
// command lists.)
type mcpListRow struct {
	Name        string   `json:"name"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	Backend     string   `json:"backend"`
	Trusted     bool     `json:"trusted"`
	TrustSource string   `json:"trust_source"`
	State       string   `json:"state"`
}

// mcpListJSON is the top-level --format json payload for `ctxloom mcp list`.
type mcpListJSON struct {
	Servers      []mcpListRow `json:"servers"`
	AutoRegister bool         `json:"auto_register"`
}

func runMCPList(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.ListMCPServers(cmd.Context(), cfg, operations.ListMCPServersRequest{
		SortBy: "name",
	})
	if err != nil {
		return err
	}

	// Effective trust is stamped only for the machine (json) surface, matching
	// the fragment/prompt listings; the human output below is unchanged.
	rows := mcpListRows(cfg, result.Servers, outputFormatOf(cmd) == formatJSON)

	return emit(cmd, mcpListJSON{Servers: rows, AutoRegister: result.AutoRegister}, func() error {
		return printMCPList(cmd.OutOrStdout(), result)
	})
}

// mcpListRows projects each configured server into its json row, stamping the
// row's effective trust (TR3) in the same iteration that reads the server when
// withTrust is set. A single TrustStamper reads the trust store / remote
// registry once for the whole list; each configured server is hashed by its
// executable surface (BundleMCP.ComputeContentHash) and resolved as a local mcp
// item.
//
// One loop over ONE slice is the invariant: a row and the server it describes
// are produced together, so there is no second slice for an index to drift
// against and no length relationship for a caller to have to maintain.
func mcpListRows(cfg *config.Config, servers []operations.MCPServerEntry, withTrust bool) []mcpListRow {
	var stamper *operations.TrustStamper
	if withTrust {
		stamper = operations.NewTrustStamper(cfg)
	}
	rows := make([]mcpListRow, 0, len(servers))
	for _, srv := range servers {
		row := mcpListRow{
			Name:    srv.Name,
			Command: srv.Command,
			Args:    srv.Args,
			Backend: srv.Backend,
		}
		if stamper != nil {
			res := stamper.ForLocalMCP(srv.Name, bundles.BundleMCP{
				Command:      srv.Command,
				Args:         srv.Args,
				Env:          srv.Env,
				Installation: srv.Installation,
			})
			row.Trusted = res.Trusted()
			row.TrustSource = string(res.Source)
			row.State = string(res.State())
		}
		rows = append(rows, row)
	}
	return rows
}

// printMCPList writes the human-readable MCP server listing, preserving the
// pre-TR3 text output exactly (trust is shown only in --format json).
func printMCPList(w io.Writer, result *operations.ListMCPServersResult) error {
	if result.Count == 0 {
		fmt.Fprintln(w, "No MCP servers configured.")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Auto-register ctxloom MCP server: %v\n", result.AutoRegister)
		fmt.Fprintln(w, "\nUse 'ctxloom mcp server create <name> --command <cmd>' to add one.")
		return nil
	}

	fmt.Fprintln(w, "MCP Servers:")
	for _, srv := range result.Servers {
		fmt.Fprintf(w, "  %s\n", srv.Name)
		fmt.Fprintf(w, "    Command: %s\n", srv.Command)
		if len(srv.Args) > 0 {
			fmt.Fprintf(w, "    Args: %s\n", strings.Join(srv.Args, " "))
		}
		fmt.Fprintf(w, "    Scope: %s\n", srv.Backend)
	}

	fmt.Fprintf(w, "\nAuto-register ctxloom MCP server: %v\n", result.AutoRegister)
	return nil
}

var (
	mcpAddCommand string
	mcpAddArgs    []string
	mcpAddBackend string
)

// mcpServerCreateLong documents `mcp server create`, the canonical spine's
// `create` for the MCP-server noun.
const mcpServerCreateLong = `Add an MCP server to be injected into backend settings.

Examples:
  ctxloom mcp server create my-server --command "npx my-mcp-server"
  ctxloom mcp server create tools --command "python" --args "-m,mcp_tools"
  ctxloom mcp server create claude-only --command "./server" --backend claude-code`

var mcpServerCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create an MCP server configuration",
	Long:  mcpServerCreateLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPAdd,
}

func runMCPAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	if mcpAddCommand == "" {
		return fmt.Errorf("--command is required")
	}

	if _, err := GetConfigForUpdate(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.AddMCPServer(cmd.Context(), config.NewManager(), operations.AddMCPServerRequest{
		Name:    name,
		Command: mcpAddCommand,
		Args:    mcpAddArgs,
		Backend: mcpAddBackend,
	})
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error {
		scope := "unified (all backends)"
		if result.Backend != "" && result.Backend != "unified" {
			scope = result.Backend + " only"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Added MCP server %q (%s)\n", result.Name, scope)
		fmt.Fprintln(cmd.OutOrStdout(), "Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
		return nil
	})
}

var mcpRemoveBackend string

// mcpServerDeleteCmd is the canonical spine's `delete` for the MCP-server noun.
var mcpServerDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete an MCP server configuration",
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPRemove,
}

func runMCPRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	if _, err := GetConfigForUpdate(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.RemoveMCPServer(cmd.Context(), config.NewManager(), operations.RemoveMCPServerRequest{
		Name:    name,
		Backend: mcpRemoveBackend,
	})
	if err != nil {
		return err
	}

	return emit(cmd, result, func() error {
		for _, backend := range result.RemovedFrom {
			if backend != "unified" {
				fmt.Fprintf(cmd.OutOrStdout(), "Removed from backend: %s\n", backend)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed MCP server %q\n", result.Name)
		fmt.Fprintln(cmd.OutOrStdout(), "Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
		return nil
	})
}

// mcpServerShowCmd is the canonical spine's `show` for the MCP-server noun.
var mcpServerShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of an MCP server configuration",
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPShow,
}

func runMCPShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	result, err := operations.GetMCPServer(cmd.Context(), cfg, operations.GetMCPServerRequest{Name: name})
	if err != nil {
		return err
	}
	if !result.Found {
		// This check used to live INSIDE emit()'s text closure,
		// which cliemit.Emit only runs for FormatText — every other format
		// fell through to clifmt.Render(result, format), rendering
		// {"found":false,...} and exiting 0. Hoisted above emit() so every
		// format sees the identical error.
		return fmt.Errorf("MCP server %q not found", name)
	}

	if err := emit(cmd, result, func() error {
		out := cmd.OutOrStdout()
		for _, e := range result.Entries {
			printMCPServerEntry(out, e)
		}
		return nil
	}); err != nil {
		return err
	}

	// Interactive trust review. TTY-gated and json-suppressed so the entry
	// output above is byte-for-byte unchanged; the review goes to stderr.
	// result.Found is guaranteed true here (the early return above handles
	// !Found for every format). These are configured-local servers
	// (first-party, no SetItemTrust ref path), so the surface reviews the
	// posture rather than offering a t/b action — see reviewLocalMCPTrust.
	if mcpShowInteractive && outputFormatOf(cmd) != formatJSON && isInteractiveTerminal() {
		reviewLocalMCPTrust(cmd, cfg, result.Entries)
	}
	return nil
}

var mcpShowInteractive bool

// printMCPServerEntry writes one MCP server entry's scope, command, args, and
// env to w.
func printMCPServerEntry(w io.Writer, e operations.MCPServerEntry) {
	fmt.Fprintf(w, "MCP Server: %s\n", e.Name)
	fmt.Fprintf(w, "Scope: %s\n", mcpScopeLabel(e.Backend))
	fmt.Fprintf(w, "Command: %s\n", e.Command)
	if len(e.Args) > 0 {
		fmt.Fprintf(w, "Args: %s\n", strings.Join(e.Args, " "))
	}
	if len(e.Env) > 0 {
		fmt.Fprintln(w, "Environment:")
		for k, v := range e.Env {
			fmt.Fprintf(w, "  %s=%s\n", k, v)
		}
	}
}

// mcpScopeLabel renders an entry's backend scope for human output, matching the
// labels the previous direct-config lookup used.
func mcpScopeLabel(backend string) string {
	if backend == "unified" {
		return "unified (all backends)"
	}
	return backend + " only"
}

// mcpServerEditCmd is the canonical spine's `edit` for the MCP-server noun. It
// is the new home of the deleted `bundle mcp edit` (CLI verb-spine reorg §5):
// the ref SHAPE picks the store, exactly as the addressing grammar does
// everywhere else — `<bundle>#mcp/<name>` addresses a bundle item.
var mcpServerEditCmd = &cobra.Command{
	Use:   "edit <bundle>#mcp/<name>",
	Short: "Edit an MCP server configuration in $EDITOR",
	Long: `Edit an MCP server's configuration using your configured editor.

Opens the MCP server config as YAML in your editor. When you save and close,
the containing bundle is updated with the new configuration.

The ref shape selects the store, per the universal addressing grammar:
a '<bundle>#mcp/<name>' ref addresses a bundle-scoped server.

Examples:
  ctxloom mcp server edit my-bundle#mcp/tree-sitter
  ctxloom mcp server edit tools#mcp/sequential-thinking`,
	Args: cobra.ExactArgs(1),
	RunE: runMCPServerEdit,
}

// mcpServerRefPrefix is the item-ref path prefix that addresses a
// bundle-scoped MCP server (`<bundle>#mcp/<name>`). Unlike fragments and
// commands the kind is not pluralized, so this cannot go through
// itemRefPrefix.
const mcpServerRefPrefix = "mcp/"

// splitMCPServerRef splits a `<bundle>#mcp/<name>` ref into its two halves.
// A ref that does not carry the `#mcp/` path is rejected by name: the
// config-level (config.yaml `mcp.servers`) store has no editor path yet, and
// silently editing the wrong store — or reporting success having changed
// nothing — is the failure mode this refusal exists to prevent.
func splitMCPServerRef(ref string) (bundleName, serverName string, err error) {
	hash := strings.Index(ref, "#")
	if hash < 0 || !strings.HasPrefix(ref[hash+1:], mcpServerRefPrefix) {
		return "", "", fmt.Errorf("mcp server edit: %q is not a bundle-scoped ref (expected <bundle>#%s<name>); editing a config-level server is not implemented — use `ctxloom mcp server delete` then `ctxloom mcp server create`", ref, mcpServerRefPrefix)
	}
	bundleName = ref[:hash]
	serverName = strings.TrimPrefix(ref[hash+1:], mcpServerRefPrefix)
	if bundleName == "" || serverName == "" {
		return "", "", fmt.Errorf("mcp server edit: incomplete ref %q (expected <bundle>#%s<name>)", ref, mcpServerRefPrefix)
	}
	return bundleName, serverName, nil
}

func runMCPServerEdit(cmd *cobra.Command, args []string) error {
	bundleName, serverName, err := splitMCPServerRef(args[0])
	if err != nil {
		return err
	}
	return runBundleMCPEdit(cmd, []string{bundleName, serverName})
}

// mcpServerCmd is the MCP-server noun: the canonical spine over the
// config-level MCP server store.
var mcpServerCmd = groupNode(&cobra.Command{
	Use:   "server",
	Short: "List, show, create, edit, or delete configured MCP servers",
})

// mcpRegisterCmd / mcpUnregisterCmd toggle ctxloom's own auto-registration,
// sharing setMcpAutoRegister (manage.go).
var mcpRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Enable auto-registration of ctxloom's own MCP server",
	Args:  cobra.NoArgs,
	RunE:  runMCPRegister,
}

var mcpUnregisterCmd = &cobra.Command{
	Use:   "unregister",
	Short: "Disable auto-registration of ctxloom's own MCP server",
	Args:  cobra.NoArgs,
	RunE:  runMCPUnregister,
}

func runMCPRegister(cmd *cobra.Command, _ []string) error {
	return setMcpAutoRegister(cmd, true)
}

func runMCPUnregister(cmd *cobra.Command, _ []string) error {
	return setMcpAutoRegister(cmd, false)
}

func init() {
	rootCmd.AddCommand(mcpCmd)

	// The bare `ctxloom mcp` (and `mcp serve`) is the runtime server, referenced
	// by generated .mcp.json. Server-config management lives under `mcp server`;
	// ctxloom's own auto-registration under `mcp register`/`mcp unregister`.
	mcpCmd.AddCommand(mcpServeCmd)

	mcpCmd.AddCommand(mcpServerCmd)
	mcpServerCmd.AddCommand(mcpServerListCmd)
	mcpServerCmd.AddCommand(mcpServerShowCmd)
	mcpServerCmd.AddCommand(mcpServerCreateCmd)
	mcpServerCmd.AddCommand(mcpServerEditCmd)
	mcpServerCmd.AddCommand(mcpServerDeleteCmd)

	mcpServerCreateCmd.Flags().StringVarP(&mcpAddCommand, "command", "c", "", "Command to run the MCP server (required)")
	mcpServerCreateCmd.Flags().StringSliceVarP(&mcpAddArgs, "args", "a", nil, "Arguments for the command (can be repeated)")
	mcpServerCreateCmd.Flags().StringVarP(&mcpAddBackend, "backend", "b", "", "Backend to add server for (claude-code, antigravity, or unified)")
	_ = mcpServerCreateCmd.MarkFlagRequired("command")

	mcpServerDeleteCmd.Flags().StringVarP(&mcpRemoveBackend, "backend", "b", "", "Backend to delete the server from")

	mcpServerShowCmd.Flags().BoolVarP(&mcpShowInteractive, "interactive", "i", false, "Review the server's effective trust (interactive terminal only)")

	mcpCmd.AddCommand(mcpRegisterCmd)
	mcpCmd.AddCommand(mcpUnregisterCmd)
}
