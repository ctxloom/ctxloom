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
const mcpServerInstructions = `Per-project task tracking. Tasks are keyed by harp IDs (e.g. "swift-amber-falcon") in an append-only per-project log. Use task_list to read (echo a task's harp_id back when referencing it later), task_add to create, task_set_status to move ("Done" completes; "Deferred" with a trigger parks a task on a revive condition), and task_edit to replace a task's text. The same store is scriptable via the ` + "`taskloom`" + ` CLI.`

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the task tools over MCP on stdio",
	Long: `Run an MCP server on stdio exposing the task store to agents:
task_list, task_add, task_set_status, and task_edit. The project and session
are resolved per call from CTXLOOM_PROJECT_ID / CTXLOOM_SESSION_HARP (exported
by ctxloom run) or the working directory, so one long-lived server follows the
session it was launched for.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		server := mcp.NewServer(&mcp.Implementation{
			Name:    "taskloom",
			Version: version,
		}, &mcp.ServerOptions{Instructions: mcpServerInstructions})
		registerTaskTools(server)

		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
