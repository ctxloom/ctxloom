package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
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
	"Session compaction keeps this sentence and discards the output itself, so " +
	"anything you do not say here is not recoverable later."

var hookToolReflectCmd = &cobra.Command{
	Use:    "tool-reflect",
	Hidden: true, // Machine callback (post_tool hook) - not for direct use
	Short:  "Prompt for a finding after a large tool result",
	Long: `Reads a post_tool hook payload on stdin, through the codec of the engine
--engine names, and, when the tool result is at least --min-output-bytes,
answers with a reminder asking the agent to state what it learned.

Compaction reduces a tool result to its shape (byte and line counts) because
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

// runHookToolReflect never reports a nonzero exit: a post_tool hook that
// fails interrupts the tool call it rode on. A payload the firing engine's
// codec cannot read is NAMED on the diagnostic channel and answered with
// silence: a hook that fired on everything it could not parse would be
// loudest exactly where it understood least.
func runHookToolReflect(cmd *cobra.Command, args []string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "ctxloom hook tool-reflect: panic: %v\n", r)
			err = nil
		}
	}()
	kind, err := firingEngine(cmd)
	if err != nil {
		clidiag.Warn("ctxloom hook tool-reflect", "%v", err)
		return nil
	}
	codec := kind.Hooks()
	var resp engine.HookResponse
	ev, err := readHookEvent(cmd, codec, wire.HookEventPostTool)
	if err != nil {
		clidiag.Warn("ctxloom hook tool-reflect", "%v", err)
	} else {
		resp = toolReflectResponse(ev, toolReflectMinBytes)
	}
	if err := writeHookResponse(cmd, codec, wire.HookEventPostTool, resp); err != nil {
		clidiag.Warn("ctxloom hook tool-reflect", "failed to answer the hook: %v", err)
	}
	return nil
}

// toolReflectResponse decides whether one post_tool event earns a reminder:
// a tool response of at least minBytes does. Split from runHookToolReflect so
// the decision is testable without a process, and so the stdin/stdout
// plumbing has nothing to get wrong.
func toolReflectResponse(ev engine.HookEvent, minBytes int) engine.HookResponse {
	if minBytes <= 0 || len(ev.ToolResponse) < minBytes {
		return engine.HookResponse{}
	}
	return engine.HookResponse{Context: ToolReflectReminder}
}

func init() {
	hookToolReflectCmd.Flags().IntVar(&toolReflectMinBytes, "min-output-bytes", 0,
		"emit the reminder only when the tool result is at least this many bytes")
	hookCmd.AddCommand(hookToolReflectCmd)
}
