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
    Sessions: compact_session, load_session, get_previous_session, recover_session
    Agents:   agent_run, agent_send, agent_recv (delegated child sessions +
              the in-memory coordinator/executor message bus)

  Read-only listings (fragments, profiles, prompts, remotes, mcp-servers,
  sessions) are exposed as MCP resources (ctxloom://...), not tools. All
  management (bundles, remotes, review/approve, trust, pinning) is done with
  the ctxloom CLI, not MCP tools. Task tracking moved to the standalone
  taskloom binary; its MCP server ('taskloom mcp') serves the task_* tools.

Manage configured MCP servers and ctxloom's own auto-registration under
'ctxloom manage mcp'.`,
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
	RunE:  runMCPServerSDK,
}

var mcpListCmd = &cobra.Command{
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

	rows := make([]mcpListRow, 0, len(result.Servers))
	for _, srv := range result.Servers {
		rows = append(rows, mcpListRow{
			Name:    srv.Name,
			Command: srv.Command,
			Args:    srv.Args,
			Backend: srv.Backend,
		})
	}
	// Stamp effective trust only for the machine (json) surface, matching the
	// fragment/prompt listings; the human output below is unchanged.
	if outputFormatOf(cmd) == formatJSON {
		stampMCPTrust(cfg, result.Servers, rows)
	}

	return emit(cmd, mcpListJSON{Servers: rows, AutoRegister: result.AutoRegister}, func() error {
		return printMCPList(cmd.OutOrStdout(), result)
	})
}

// stampMCPTrust annotates each json row with its effective trust (TR3). A single
// TrustStamper reads the trust store / remote registry once for the whole list;
// each configured server is hashed by its executable surface
// (BundleMCP.ComputeContentHash) and resolved as a local mcp item.
func stampMCPTrust(cfg *config.Config, servers []operations.MCPServerEntry, rows []mcpListRow) {
	stamper := operations.NewTrustStamper(cfg)
	for i := range rows {
		srv := servers[i]
		res := stamper.ForLocalMCP(srv.Name, bundles.BundleMCP{
			Command:      srv.Command,
			Args:         srv.Args,
			Env:          srv.Env,
			Installation: srv.Installation,
		})
		rows[i].Trusted = res.Trusted()
		rows[i].TrustSource = string(res.Source)
		rows[i].State = string(res.State())
	}
}

// printMCPList writes the human-readable MCP server listing, preserving the
// pre-TR3 text output exactly (trust is shown only in --format json).
func printMCPList(w io.Writer, result *operations.ListMCPServersResult) error {
	if result.Count == 0 {
		fmt.Fprintln(w, "No MCP servers configured.")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Auto-register ctxloom MCP server: %v\n", result.AutoRegister)
		fmt.Fprintln(w, "\nUse 'ctxloom manage mcp servers add <name> --command <cmd>' to add one.")
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

var mcpAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add an MCP server configuration",
	Long: `Add an MCP server to be injected into backend settings.

Examples:
  ctxloom manage mcp servers add my-server --command "npx my-mcp-server"
  ctxloom manage mcp servers add tools --command "python" --args "-m,mcp_tools"
  ctxloom manage mcp servers add claude-only --command "./server" --backend claude-code`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if mcpAddCommand == "" {
			return fmt.Errorf("--command is required")
		}

		cfg, err := GetConfigForUpdate()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		result, err := operations.AddMCPServer(cmd.Context(), cfg, operations.AddMCPServerRequest{
			Name:    name,
			Command: mcpAddCommand,
			Args:    mcpAddArgs,
			Backend: mcpAddBackend,
		})
		if err != nil {
			return err
		}

		scope := "unified (all backends)"
		if result.Backend != "" && result.Backend != "unified" {
			scope = result.Backend + " only"
		}
		fmt.Printf("Added MCP server %q (%s)\n", result.Name, scope)
		fmt.Println("Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
		return nil
	},
}

var mcpRemoveBackend string

var mcpRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove an MCP server configuration",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfigForUpdate()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		result, err := operations.RemoveMCPServer(cmd.Context(), cfg, operations.RemoveMCPServerRequest{
			Name:    name,
			Backend: mcpRemoveBackend,
		})
		if err != nil {
			return err
		}

		for _, backend := range result.RemovedFrom {
			if backend != "unified" {
				fmt.Printf("Removed from backend: %s\n", backend)
			}
		}

		fmt.Printf("Removed MCP server %q\n", result.Name)
		fmt.Println("Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
		return nil
	},
}

var mcpShowCmd = &cobra.Command{
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

	if err := emit(cmd, result, func() error {
		if !result.Found {
			return fmt.Errorf("MCP server %q not found", name)
		}
		out := cmd.OutOrStdout()
		for _, e := range result.Entries {
			printMCPServerEntry(out, e)
		}
		return nil
	}); err != nil {
		return err
	}

	// Interactive trust review. TTY-gated and json-suppressed so the entry
	// output above is byte-for-byte unchanged; the review goes to stderr. These
	// are configured-local servers (first-party, no SetItemTrust ref path), so
	// the surface reviews the posture rather than offering a t/b action — see
	// reviewLocalMCPTrust.
	if mcpShowInteractive && result.Found && outputFormatOf(cmd) != formatJSON && isInteractiveTerminal() {
		reviewLocalMCPTrust(cfg, result.Entries)
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

func init() {
	rootCmd.AddCommand(mcpCmd)

	// The bare `ctxloom mcp` (and `mcp serve`) is the runtime server, referenced
	// by generated .mcp.json. Server-config management lives under `manage mcp`
	// (wired in manage.go); only the runtime entry hangs off root here.
	mcpCmd.AddCommand(mcpServeCmd)

	// Flags for the server-config CRUD commands (re-parented under manage mcp).
	mcpAddCmd.Flags().StringVarP(&mcpAddCommand, "command", "c", "", "Command to run the MCP server (required)")
	mcpAddCmd.Flags().StringSliceVarP(&mcpAddArgs, "args", "a", nil, "Arguments for the command (can be repeated)")
	mcpAddCmd.Flags().StringVarP(&mcpAddBackend, "backend", "b", "", "Backend to add server for (claude-code, antigravity, or unified)")
	_ = mcpAddCmd.MarkFlagRequired("command")

	mcpRemoveCmd.Flags().StringVarP(&mcpRemoveBackend, "backend", "b", "", "Backend to remove server from")

	mcpShowCmd.Flags().BoolVarP(&mcpShowInteractive, "interactive", "i", false, "Review the server's effective trust (interactive terminal only)")
}
