package cli

import (
	"encoding/json"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// stampPlanCmd reads a PostToolUse file-edit hook payload on stdin
// (Claude Code's Edit|Write shape) and, when the edited file
// matches the plan-file pattern, stamps the active session's harp name
// into the file's YAML frontmatter. No-op when CTXLOOM_SESSION_HARP is
// unset or the edited file isn't a plan file.
var stampPlanCmd = &cobra.Command{
	Use:    "stamp-plan",
	Short:  "Stamp the active session's harp name into a plan file's frontmatter (internal — used by the PostFileEdit hook)",
	Hidden: true,
	RunE:   runStampPlan,
}

func runStampPlan(cmd *cobra.Command, args []string) error {
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
		clidiag.Warn("ctxloom", "stamp-plan: read stdin: %v", err)
		return nil
	}
	path, err := parseEditPayload(raw)
	if err != nil {
		// A payload this hook cannot decode is a contract break with the
		// host engine (every supported shape is JSON), so it is reported
		// on the same warn-and-continue channel as the stdin-read failure
		// above rather than vanishing. A payload that decodes but names no
		// file (a non-edit tool call) is an ordinary event and stays
		// silent, so this never becomes per-tool-call noise.
		clidiag.Warn("ctxloom", "stamp-plan: parse hook payload: %v", err)
		return nil
	}
	if path == "" {
		return nil // no file_path — not a file edit, nothing to stamp
	}
	if !memory.IsPlanFile(path) {
		return nil
	}
	if err := memory.StampPlanFile(path, harp); err != nil {
		clidiag.Warn("ctxloom", "stamp-plan: %v", err)
	}
	return nil
}

func init() {
	// stamp-plan is a machine callback (PostFileEdit hook target), so it lives
	// under the hidden `hook` namespace.
	hookCmd.AddCommand(stampPlanCmd)
}

// parseEditPayload extracts the edited file's path from a file-edit hook
// payload: Claude Code's tool_input.file_path (wrapped or bare).
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
	return "", nil
}
