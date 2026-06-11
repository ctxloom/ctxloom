package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/antigravity"
	"github.com/ctxloom/ctxloom/internal/memory"
)

// stampPlanCmd reads a PostToolUse file-edit hook payload on stdin
// (Claude Code's Edit|Write shape or Antigravity's
// write_to_file|replace_file_content shape) and, when the edited file
// matches the plan-file pattern, stamps the active session's harp name
// into the file's YAML frontmatter. No-op when CTXLOOM_SESSION_HARP is
// unset or the edited file isn't a plan file.
var stampPlanCmd = &cobra.Command{
	Use:    "stamp-plan",
	Short:  "Stamp the active session's harp name into a plan file's frontmatter (internal — used by the PostFileEdit hook)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		harp := os.Getenv("CTXLOOM_SESSION_HARP")
		if harp == "" {
			// No active session — silent no-op so the hook is safe to
			// install before Phase 3's session naming ships.
			return nil
		}
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			// Machine hook: never fail the host agent's tool call over a
			// stamping hiccup (the sibling hooks — session-bind,
			// inject-context — follow the same warn-and-continue rule).
			fmt.Fprintf(os.Stderr, "ctxloom: warning: stamp-plan: read stdin: %v\n", err)
			return nil
		}
		path, err := parseEditPayload(raw)
		if err != nil || path == "" {
			return nil // malformed or no file_path — silent no-op
		}
		if !memory.IsPlanFile(path) {
			return nil
		}
		if err := memory.StampPlanFile(path, harp); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: stamp-plan: %v\n", err)
		}
		return nil
	},
}

func init() {
	// stamp-plan is a machine callback (PostFileEdit hook target), so it lives
	// under the hidden `hook` namespace.
	hookCmd.AddCommand(stampPlanCmd)
}

// parseEditPayload extracts the edited file's path from a file-edit hook
// payload: Claude Code's tool_input.file_path (wrapped or bare) or
// Antigravity's toolCall.args.TargetFile (decoded via the antigravity
// module's wire types, the contract's single source of truth).
func parseEditPayload(raw []byte) (string, error) {
	type input struct {
		FilePath string `json:"file_path"`
	}
	type wrapper struct {
		ToolInput *input `json:"tool_input"`
		input
	}
	var w wrapper
	if err := json.Unmarshal(raw, &w); err != nil {
		return "", err
	}
	if w.ToolInput != nil && w.ToolInput.FilePath != "" {
		return w.ToolInput.FilePath, nil
	}
	if w.FilePath != "" {
		return w.FilePath, nil
	}
	if p, err := antigravity.DecodeHookPayload(raw); err == nil {
		return p.ToolCall.Args.FilePath(), nil
	}
	return "", nil
}
