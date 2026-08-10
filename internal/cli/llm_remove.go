package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var llmRemoveYes bool

// llmRemoveCmd is `llm remove` — the third CRUD verb closing the parity gap
// with `agent remove`. Bare invocation reports what would be removed and
// removes nothing (exit 0); --yes applies it — the same posture every
// `remove` leaf in this CLI shares (remove_preview.go).
var llmRemoveCmd = &cobra.Command{
	Use:     "remove <label>",
	Aliases: []string{"rm", "del"},
	Short:   "Remove an LLM engine config from config.yaml",
	Long: `Remove a labeled LLM engine config from the 'llm.configs' key of
.ctxloom/config.yaml.

Bare invocation reports what would be removed and removes nothing (exit 0).
Pass --yes to apply it.

This does NOT touch any credential recorded for the label in your home
config (~/.ctxloom/config.yaml, llm.configs.<label>.env — machine-scoped,
and potentially still used by another project binding the same label).`,
	Args: cobra.ExactArgs(1),
	RunE: runLLMRemove,
}

func runLLMRemove(cmd *cobra.Command, args []string) error {
	label := args[0]
	cfg, err := GetConfigForUpdate()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	applyCmd := fmt.Sprintf("ctxloom llm remove %s --yes", label)
	if !llmRemoveYes {
		if !cfg.IsLLMUserAuthored(label) {
			return fmt.Errorf("llm %q is not defined in config.yaml", label)
		}
		target := fmt.Sprintf("llm %q", label)
		return emit(cmd, newRemovePreviewResult(target, nil, applyCmd), func() error {
			printRemovePreview(cmd.OutOrStdout(), target, nil, applyCmd)
			return nil
		})
	}

	if err := operations.RemoveLLM(config.NewManager(), cfg, label); err != nil {
		return err
	}
	return emit(cmd, struct {
		Status string `json:"status"`
		Label  string `json:"label"`
	}{Status: "removed", Label: label}, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Removed llm %q\n", label)
		return w.Err()
	})
}

func init() {
	llmCmd.AddCommand(llmRemoveCmd)
	llmRemoveCmd.Flags().BoolVarP(&llmRemoveYes, "yes", "y", false, "Apply the removal this invocation would report (default: report only)")
}
