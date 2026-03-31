package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/gitutil"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// HookInput represents the JSON input from AI tool hooks.
// Claude Code provides session_id; Gemini CLI provides transcript_path directly.
type HookInput struct {
	SessionID      string `json:"session_id"`      // Claude Code: session identifier
	TranscriptPath string `json:"transcript_path"` // Gemini CLI: full path to transcript file
}

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

var injectContextProject string
var injectContextBackend string

var hookInjectContextCmd = &cobra.Command{
	Use:   "inject-context <hash>",
	Short: "Inject session context for AI tool hooks",
	Long: `Reads the context file (.ctxloom/context/<hash>.md) and outputs JSON suitable for
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
				fmt.Fprintf(os.Stderr, "ctxloom hook inject-context: panic: %v\n", r)
				// Output empty JSON on panic
				fmt.Println("{}")
			}
		}()

		// Read hook input from stdin (Claude passes session context here)
		var hookInput HookInput
		inputData, err := io.ReadAll(os.Stdin)
		if err == nil && len(inputData) > 0 {
			if unmarshalErr := json.Unmarshal(inputData, &hookInput); unmarshalErr != nil {
				fmt.Fprintf(os.Stderr, "ctxloom hook inject-context: warning: failed to parse hook input: %v\n", unmarshalErr)
			}
		}

		// Determine work directory from --project flag, git root, or current directory
		workDir := injectContextProject
		if workDir == "" {
			// Fallback to git root or current directory
			if root, err := gitutil.FindRoot("."); err == nil {
				workDir = root
			} else {
				workDir = "."
			}
		}

		// Register session for /clear recovery
		backend := backends.Get(injectContextBackend)
		if backend == nil {
			backend = backends.Get("claude-code") // default
		}
		if history := backend.History(); history != nil {
			transcriptPath := history.TranscriptPathFromHook(workDir, hookInput.SessionID, hookInput.TranscriptPath)
			if transcriptPath != "" {
				pid := findSCMWrapperPID()
				if err := history.RegisterSession(workDir, pid, transcriptPath); err != nil {
					fmt.Fprintf(os.Stderr, "ctxloom hook inject-context: warning: failed to register session: %v\n", err)
				}
			}
		}

		// Read context file by hash
		content, err := backends.ReadContextFile(workDir, hash)
		if err != nil {
			// Log to stderr, output empty JSON to stdout
			fmt.Fprintf(os.Stderr, "ctxloom hook inject-context: warning: failed to read context file: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "ctxloom hook inject-context: warning: failed to encode output: %v\n", err)
			fmt.Println("{}")
		}
		return nil
	},
}

func init() {
	hookInjectContextCmd.Flags().StringVar(&injectContextProject, "project", "", "Project directory (defaults to git root or current directory)")
	hookInjectContextCmd.Flags().StringVar(&injectContextBackend, "backend", "claude-code", "Backend type (claude-code or gemini)")
	hookCmd.AddCommand(hookInjectContextCmd)
}
