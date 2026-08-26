package cli

import (
	"context"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/mcp"
)

// runMCPServerSDK is the cobra RunE for `ctxloom mcp serve`. The SDK's
// Server.Run handles its own stdin EOF and ctx-cancellation cleanup — we
// just need to give it a signal-aware context. signal.NotifyContext is
// the idiomatic Go-1.16+ replacement for manual sigCh + cancel goroutines.
//
// This is all that stays in internal/cli of the former MCP server: the whole
// serve body is mcp.ServeStdio now. It stays because of the fail-loudly gate
// it hands down — failOnFindings constructs the ExitError that Execute()
// turns into exit 3, a cli-layer contract package mcp must not reach for.
func runMCPServerSDK(_ *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		cwd = "."
	}

	// Fail-loudly gate: checkpoint before the boot sequence so every
	// fatal-class finding it records is caught by the gate below.
	gates := newPhaseGates(os.Stderr)

	return mcp.ServeStdio(ctx, cwd, func() error {
		return gates.close(PhaseStartup)
	})
}
