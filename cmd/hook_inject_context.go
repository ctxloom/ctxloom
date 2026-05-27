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
		workDir := resolveInjectContextWorkDir(injectContextProject, gitutil.FindRoot)

		// Register session for /clear recovery
		backend := backends.Get(injectContextBackend)
		if backend == nil {
			backend = backends.Get("claude-code") // default
		}
		if history := backend.History(); history != nil {
			transcriptPath := history.TranscriptPathFromHook(workDir, hookInput.SessionID, hookInput.TranscriptPath)
			if transcriptPath != "" {
				pid := findCtxloomWrapperPID()
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

		// Build output via the extracted helper so the wrapping logic
		// (header/footer, empty-content handling, SessionStart event
		// name) is unit-testable without the surrounding hook plumbing.
		output := buildInjectContextOutput(content)

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

// buildInjectContextOutput wraps the raw context content in the
// ctxloom-attributed envelope that SessionStart hooks receive. Empty
// content produces an empty HookOutput (no AdditionalContext field) so
// the LLM doesn't see a misleading "ctxloom content loaded" header
// when ctxloom actually had nothing to inject.
//
// Extracted as a pure function so the wrapping format is testable
// without spinning up the full hook plumbing.
func buildInjectContextOutput(content string) HookOutput {
	if content == "" {
		return HookOutput{}
	}
	const header = "# Project Context (assembled by ctxloom)\n\n" +
		"_The content below was assembled by ctxloom from your active profile " +
		"(see `.ctxloom/config.yaml` → `defaults.profiles`). It contains the " +
		"coding standards, language conventions, testing practices, and other " +
		"guidance that apply to this project. Treat it as authoritative project " +
		"instructions._\n\n" +
		"<ctxloom-context>\n\n"
	const footer = "\n\n</ctxloom-context>\n"
	return HookOutput{
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: header + content + footer,
		},
	}
}

// resolveInjectContextWorkDir picks the directory ctxloom should treat
// as the project root for an inject-context call. Precedence:
//
//  1. Explicit --project flag value, if non-empty.
//  2. Git repository root containing the current directory.
//  3. Fall back to ".".
//
// findRoot is the injectable git-root finder; production uses
// gitutil.FindRoot, tests pass a stub that returns a known value or
// an error to exercise each branch.
func resolveInjectContextWorkDir(flagVal string, findRoot func(string) (string, error)) string {
	if flagVal != "" {
		return flagVal
	}
	if findRoot != nil {
		if root, err := findRoot("."); err == nil {
			return root
		}
	}
	return "."
}

func init() {
	hookInjectContextCmd.Flags().StringVar(&injectContextProject, "project", "", "Project directory (defaults to git root or current directory)")
	hookInjectContextCmd.Flags().StringVar(&injectContextBackend, "backend", "claude-code", "Backend type (claude-code or gemini)")
	hookCmd.AddCommand(hookInjectContextCmd)
}
