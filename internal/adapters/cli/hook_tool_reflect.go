package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// toolReflectMinBytes is the --min-output-bytes flag: the tool-result size at
// or above which the reminder fires. The installed hook command carries the
// config-resolved value, so the flag is the transport, not a second policy.
var toolReflectMinBytes int

// ToolReflectReminder is the text injected after a large tool result.
//
// It is a constant so the acceptance and unit assertions bind to the same
// bytes the hook emits rather than to a copy that can drift out of agreement
// with it.
const ToolReflectReminder = "That tool result was large. Before your next tool call, state what you " +
	"actually learned from it — including \"nothing\" or \"not what I expected\". " +
	"Session distillation keeps this sentence and discards the output itself, so " +
	"anything you do not say here is not recoverable later."

var hookToolReflectCmd = &cobra.Command{
	Use:    "tool-reflect",
	Hidden: true, // Machine callback (PostToolUse hook) - not for direct use
	Short:  "Prompt for a finding after a large tool result",
	Long: `Reads a PostToolUse hook payload on stdin and, when the tool result is at
least --min-output-bytes, emits an additionalContext reminder asking the agent
to state what it learned.

Distillation reduces a tool result to its shape (byte and line counts) because
a truncated fragment of one is neither the information nor a summary of it. The
agent's own statement of what it learned is the part that survives, and the part
nothing else can reconstruct.

The hook is silent below the threshold, which is the common case: most tool
results are small enough that their shape describes them adequately.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookToolReflect,
}

func runHookToolReflect(cmd *cobra.Command, args []string) (err error) {
	// A hook that panics or errors must still leave valid JSON on stdout, or
	// the host is left parsing nothing. Silence is the safe output here: it
	// injects no context and blocks nothing.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "ctxloom hook tool-reflect: panic: %v\n", r)
			fmt.Println("{}")
			err = nil
		}
	}()

	raw, readErr := io.ReadAll(cmd.InOrStdin())
	if readErr != nil {
		clidiag.Warn("ctxloom hook tool-reflect", "failed to read hook input: %v", readErr)
		fmt.Println("{}")
		return nil
	}

	out := buildToolReflectOutput(raw, toolReflectMinBytes)
	if encErr := json.NewEncoder(cmd.OutOrStdout()).Encode(out); encErr != nil {
		clidiag.Warn("ctxloom hook tool-reflect", "failed to encode output: %v", encErr)
		fmt.Println("{}")
	}
	return nil
}

// buildToolReflectOutput decides whether one PostToolUse payload earns a
// reminder. Split from runHookToolReflect so the decision is testable without
// a process, and so the stdin/stdout plumbing has nothing to get wrong.
//
// An undecodable payload produces silence rather than a reminder: a hook that
// fired on everything it could not parse would be loudest exactly where it
// understood least.
func buildToolReflectOutput(raw []byte, minBytes int) claude.PostToolUseOutput {
	if minBytes <= 0 {
		return claude.PostToolUseOutput{}
	}
	var payload claude.PostToolUsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return claude.PostToolUseOutput{}
	}
	if len(payload.ToolResponse) < minBytes {
		return claude.PostToolUseOutput{}
	}
	return claude.PostToolUseOutput{
		HookSpecificOutput: &claude.PostToolUseSpecificOutput{
			HookEventName:     claude.HookEventPostToolUse,
			AdditionalContext: ToolReflectReminder,
		},
	}
}

func init() {
	hookToolReflectCmd.Flags().IntVar(&toolReflectMinBytes, "min-output-bytes", 0,
		"emit the reminder only when the tool result is at least this many bytes")
	hookCmd.AddCommand(hookToolReflectCmd)
}
