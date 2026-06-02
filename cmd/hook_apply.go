package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

var hookApplyBackend string

// hookApplyCmd (re)writes ctxloom's hooks, MCP servers, and assembled context
// into the backend config files. It replaces the former apply_hooks MCP tool;
// `ctxloom remote sync` and `ctxloom init` already apply hooks, so this is the
// standalone re-apply path (e.g. after editing bundles or profiles).
var hookApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply ctxloom hooks and regenerate context into backend config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		result, err := operations.ApplyHooks(cmd.Context(), cfg, operations.ApplyHooksRequest{
			Backend:           hookApplyBackend,
			RegenerateContext: true,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Hooks %s for: %v\n", result.Status, result.Backends)
		for _, e := range result.Errors {
			fmt.Printf("  warning: %s\n", e)
		}
		return nil
	},
}

func init() {
	hookCmd.AddCommand(hookApplyCmd)
	hookApplyCmd.Flags().StringVar(&hookApplyBackend, "backend", "all", "Backend to apply hooks for (claude-code, gemini, or all)")
}
