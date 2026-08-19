package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// mcpCmd is the MCP noun. Bare `ctxloom mcp` conforms to the bare-noun ladder
// and answers with the configured MCP servers, delegating through `mcp server`
// to its own `list` — the collection is the one thing the noun is about, and
// reading it touches nothing.
//
// The machine surface is `ctxloom mcp serve`, one spelling, symmetric with
// `ctxloom acp serve`. mcpBareMachineRefusal is what keeps the two from
// colliding when a caller off a terminal types the noun on its own.
var mcpCmd = mcpBareMachineRefusal(groupNodeDefault(&cobra.Command{
	Use:   "mcp",
	Short: "List configured MCP servers, or serve ctxloom as one",
	Long: `The MCP (Model Context Protocol) noun: the servers this project hands to
every engine, and ctxloom's own stdio server.

  ctxloom mcp              List the MCP servers this project registers
  ctxloom mcp serve        Serve ctxloom AS an MCP server over stdio. This is
                           the invocation an engine's settings name, and the
                           only one that speaks the protocol.
  ctxloom mcp server       List, show and edit registered servers

Every server here comes from a BUNDLE — ctxloom's own included, which ships
in the builtin ctxloom bundle. Add one by composing a bundle that declares it;
withhold one with a profile's exclude_mcp, or with
  ctxloom bundle reject <bundle>#mcp/<name>

Tools ctxloom serves under 'mcp serve':
  Context:  assemble_context, search_content, search_library
  Sessions: compact_session, list_sessions, load_session, get_previous_session, recover_session
  Health:   context_status (this session's measured context-window occupancy)
  Agents:   agent_run, agent_send, agent_recv (delegated child sessions +
            the in-memory coordinator/executor message bus)

Read-only listings (fragments, profiles, prompts, remotes, mcp-servers,
sessions) are exposed as MCP resources (ctxloom://...), not tools. All
management (bundles, remotes, review/approve, trust, pinning) is done with
the ctxloom CLI, not MCP tools. Task tracking lives in the standalone
taskloom binary; its MCP server ('taskloom mcp') serves the task_* tools.`,
}, "server"))

// MCP subcommands for managing MCP server configurations

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve ctxloom as an MCP server over stdio",
	Long: `Serve ctxloom as an MCP (Model Context Protocol) server over stdio.

This is the machine surface: the invocation ctxloom writes into every engine's
own MCP settings, and the only spelling that speaks the protocol.`,
	// NoArgs because this RunE is the stdio server: `ctxloom mcp serve list`
	// would otherwise sit waiting on stdin instead of reporting the mistake.
	Args: cobra.NoArgs,
	RunE: runMCPServerSDK,
}

// mcpBareMachineRefusal wraps the bare noun's delegation so a caller that is
// not a person at a terminal is refused instead of answered.
//
// WHY A LISTING IS WORSE THAN AN ERROR HERE. A protocol client launches its
// configured command, opens a pipe, and waits for a JSON-RPC `initialize`
// response. Server-listing text written into that pipe cannot be framed, so
// the client neither parses nor rejects it — it waits. Exit code 0, a running
// engine, no ctxloom tools, and nothing anywhere naming the cause: this
// project's characteristic silent no-op, on the one surface a machine drives.
// An error on stderr with a non-zero status is the loud version of the same
// event, and it names the spelling that works.
//
// The encoding is deliberately not consulted. `--format json` makes a listing
// no more deliverable to a caller framing JSON-RPC, so the refusal is
// unconditional off a terminal and points a script at the leaf that produces
// the servers as data.
func mcpBareMachineRefusal(cmd *cobra.Command) *cobra.Command {
	delegate := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) == 0 && !isInteractiveTerminal() {
			return errMCPBareIsNotTheServer
		}
		return delegate(c, args)
	}
	return cmd
}

// errMCPBareIsNotTheServer is the refusal a non-terminal caller of the bare
// noun earns. A sentinel rather than a formatted string per this project's
// error-handling standard, and worded for the two readers who will see it: a
// human reading an engine's stderr log, and whoever has to fix the settings
// entry that produced it.
var errMCPBareIsNotTheServer = fmt.Errorf(
	"`ctxloom mcp` lists this project's configured MCP servers; it does not speak the protocol, " +
		"and a listing delivered to a client waiting for JSON-RPC is indistinguishable from a hang. " +
		"The stdio server is `ctxloom mcp serve` — point this client's configured command at it " +
		"(`ctxloom init` rewrites every engine's settings), or run `ctxloom mcp server list` for the listing as data")

// mcpServerListCmd is the canonical spine's `list` for the MCP-server noun.
var mcpServerListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured MCP servers",
	RunE:    runMCPList,
}

// mcpListRow is the --format json shape for `ctxloom mcp list`: one registered
// MCP server and the bundle that ships it. Source is also the server's TRUST
// identity — every entry is a bundle item, addressed <bundle>#mcp/<name>, and
// gated at the bundle exec choke — so the posture is read and changed through
// the bundle surfaces (`ctxloom bundle show -i`, `ctxloom bundle trust|reject`,
// `ctxloom review`) rather than restated here.
type mcpListRow struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Source  string   `json:"source"`
}

// mcpListJSON is the top-level --format json payload for `ctxloom mcp list`.
type mcpListJSON struct {
	Servers []mcpListRow `json:"servers"`
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

	return emit(cmd, mcpListJSON{Servers: mcpListRows(result.Servers)}, func() error {
		return printMCPList(cmd.OutOrStdout(), result)
	})
}

// mcpListRows projects each registered server into its json row.
func mcpListRows(servers []operations.MCPServerEntry) []mcpListRow {
	rows := make([]mcpListRow, 0, len(servers))
	for _, srv := range servers {
		rows = append(rows, mcpListRow{
			Name:    srv.Name,
			Command: srv.Command,
			Args:    srv.Args,
			Source:  srv.Source,
		})
	}
	return rows
}

// printMCPList writes the human-readable MCP server listing.
func printMCPList(w io.Writer, result *operations.ListMCPServersResult) error {
	if result.Count == 0 {
		fmt.Fprintln(w, "No MCP servers registered.")
		fmt.Fprintln(w, "\nMCP servers ship in bundles — compose one that declares an `mcp:` server, or check whether a profile's `exclude_mcp` withholds it.")
		return nil
	}

	fmt.Fprintln(w, "MCP Servers:")
	for _, srv := range result.Servers {
		fmt.Fprintf(w, "  %s\n", srv.Name)
		fmt.Fprintf(w, "    Command: %s\n", srv.Command)
		if len(srv.Args) > 0 {
			fmt.Fprintf(w, "    Args: %s\n", strings.Join(srv.Args, " "))
		}
		fmt.Fprintf(w, "    Bundle: %s\n", srv.Source)
	}
	return nil
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

	return nil
}

// printMCPServerEntry writes one MCP server entry's bundle, command, args, and
// env to w.
func printMCPServerEntry(w io.Writer, e operations.MCPServerEntry) {
	fmt.Fprintf(w, "MCP Server: %s\n", e.Name)
	fmt.Fprintf(w, "Bundle: %s\n", e.Source)
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

// mcpServerRefPrefix is the selector directory that addresses a bundle-scoped
// MCP server (`<bundle>#mcp/<name>`). Unlike fragments and commands the kind
// is not pluralized, so it does not go through itemRefPrefix.
const mcpServerRefPrefix = "mcp/"

// runMCPServerEdit edits a bundle-scoped MCP server named by a
// `<bundle>#mcp/<name>` ref, judged by bundles.ParseItemAsk — the one selector
// parser every reader shares.
//
// A ref that does not select an MCP server is refused by name: an MCP server
// lives in a bundle and nowhere else, so a ref that names no bundle names
// nothing this command can edit — and reporting success having changed nothing
// is the failure mode this refusal exists to prevent.
func runMCPServerEdit(cmd *cobra.Command, args []string) error {
	notBundleScoped := func() error {
		return fmt.Errorf("mcp server edit: %q is not a bundle-scoped ref (expected <bundle>#%s<name>); every MCP server lives in a bundle, so there is no other store to edit", args[0], mcpServerRefPrefix)
	}
	ask, err := bundles.ParseItemAsk(args[0])
	if err != nil {
		return notBundleScoped()
	}
	if !ask.Scoped || ask.Kind != trust.KindMCP {
		return notBundleScoped()
	}
	if ask.Bundle == "" || ask.Item == "" {
		return fmt.Errorf("mcp server edit: incomplete ref %q (expected <bundle>#%s<name>)", args[0], mcpServerRefPrefix)
	}
	return runBundleMCPEdit(cmd, []string{ask.Bundle, ask.Item})
}

// mcpServerCmd is the MCP-server noun: the canonical spine over the servers
// this project registers, every one of them a bundle item. Bare `ctxloom mcp
// server` lists them: the collection is the one thing the noun is about, and
// reading it touches nothing.
var mcpServerCmd = groupNodeDefault(&cobra.Command{
	Use:   "server",
	Short: "List, show, or edit the MCP servers this project registers",
}, "list")

func init() {
	rootCmd.AddCommand(mcpCmd)

	// `mcp serve` is the runtime server, and the invocation every generated
	// engine surface names (agent.CtxloomMCPArgs). A server's definition lives
	// in the bundle that ships it, so `mcp server` reads and edits there.
	mcpCmd.AddCommand(mcpServeCmd)

	mcpCmd.AddCommand(mcpServerCmd)
	mcpServerCmd.AddCommand(mcpServerListCmd)
	mcpServerCmd.AddCommand(mcpServerShowCmd)
	mcpServerCmd.AddCommand(mcpServerEditCmd)
}
