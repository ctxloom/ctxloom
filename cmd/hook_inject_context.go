package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/SophisticatedContextManager/scm/internal/gitutil"
	"github.com/SophisticatedContextManager/scm/internal/lm/backends"
)

// HookOutput represents the JSON output format for AI tool hooks.
// This format is compatible with both Claude Code and Gemini CLI SessionStart hooks.
type HookOutput struct {
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput contains hook-specific data to inject.
type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

var hookInjectContextCmd = &cobra.Command{
	Use:   "inject-context <hash>",
	Short: "Inject session context for AI tool hooks",
	Long: `Reads the context file (.scm/context/<hash>.md) and outputs JSON suitable for
AI tool SessionStart hooks.

This command is invoked automatically by AI tools (Claude Code, Gemini CLI) during
their SessionStart event to inject fresh context on startup, resume, or /clear.

Arguments:
  hash    The context file hash (filename without .md extension)

Output format (JSON to stdout):
{
  "hookSpecificOutput": {
    "additionalContext": "<context content>"
  }
}`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		hash := args[0]

		// Always output valid JSON, even on errors.
		// This ensures Claude doesn't hang waiting for output.
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "scm hook inject-context: panic: %v\n", r)
				// Output empty JSON on panic
				fmt.Println("{}")
			}
		}()

		// Determine work directory (git root or current directory)
		workDir := "."
		if root, err := gitutil.FindRoot("."); err == nil {
			workDir = root
		}

		// Read context file by hash
		content, err := backends.ReadContextFile(workDir, hash)
		if err != nil {
			// Log to stderr, output empty JSON to stdout
			fmt.Fprintf(os.Stderr, "scm hook inject-context: warning: failed to read context file: %v\n", err)
			content = ""
		}

		// Build output
		output := HookOutput{}
		if content != "" {
			output.HookSpecificOutput = &HookSpecificOutput{
				HookEventName:     "SessionStart",
				AdditionalContext: content,
			}
		}

		// Output JSON to stdout
		encoder := json.NewEncoder(os.Stdout)
		if err := encoder.Encode(output); err != nil {
			// If encoding fails, output empty JSON
			fmt.Fprintf(os.Stderr, "scm hook inject-context: warning: failed to encode output: %v\n", err)
			fmt.Println("{}")
		}
		return nil
	},
}

func init() {
	hookCmd.AddCommand(hookInjectContextCmd)
}
