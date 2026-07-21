package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// mcpServerInstructions tells the client what this MCP surface is for. The
// store is shared with the `taskloom` CLI; the project resolves from the session
// env (CTXLOOM_PROJECT_ID / CTXLOOM_SESSION_HARP, exported by ctxloom run) or
// the working directory.
const mcpServerInstructions = `Per-project task tracking. Tasks are keyed by harp IDs (e.g. "swift-amber-falcon") in an append-only per-project log. Use task_list to read (echo a task's harp_id back when referencing it later; filter by tag with tag_query, a postfix boolean expression like "urgent/release/and"), task_add to create (optionally with initial tags), task_tag to add or remove a task's tags, task_set_status to move ("Done" completes; "Deferred" with a trigger parks a task on a revive condition), and task_edit to replace a task's text. Tags are ` + "`(namespace:)key(=value)`" + `, not flat labels — read the ` + "`taskloom://tag-schema`" + ` and ` + "`taskloom://tag-vocabulary`" + ` resources before choosing one; a project may declare enums, numeric ranges, and scalar (at-most-one-value) targets that are enforced on write. The same store is scriptable via the ` + "`taskloom`" + ` CLI. When writing task text, make the first line the subject (what the task IS, in ~80 characters or fewer) and put provenance like dates or session names on a later line — list views show only that truncated first line.`

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the task tools over MCP on stdio",
	Long: `Run an MCP server on stdio exposing the task store to agents:
task_list, task_add, task_tag, task_set_status, and task_edit. The project and session
are resolved per call from CTXLOOM_PROJECT_ID / CTXLOOM_SESSION_HARP (exported
by ctxloom run) or the working directory, so one long-lived server follows the
session it was launched for.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		if err := newMCPServer().Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	},
}

// newMCPServer builds the task-tool MCP server: the surface `taskloom mcp`
// serves, and — because the documentation generator stands up this same
// factory — the surface the MCP reference page is generated from. Registration
// only records static tool literals and the reflected input schemas; no handler
// runs and no task log is touched, so it is safe to build for docs.
func newMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "taskloom",
		Version: version,
	}, &mcp.ServerOptions{Instructions: mcpServerInstructions})
	registerTaskTools(server)
	registerTaskResources(server)
	return server
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
