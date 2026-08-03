package cli

import (
	"github.com/spf13/cobra"
)

// utilCmd is a hidden home for agent-facing filesystem-safety primitives —
// commands an LLM session invokes on ctxloom's behalf, never a human
// directly. It follows the same convention as the hidden `hook` namespace
// (hook.go): a single hidden parent nesting narrowly-scoped internal
// subcommands, keeping the user-visible `--help` surface small while still
// giving an agent a real, tested command to call instead of hand-rolling
// dangerous mechanics itself. See config-write for the first resident.
var utilCmd = groupNode(&cobra.Command{
	Use:    "util",
	Short:  "Internal safety primitives for agents (not for direct use)",
	Hidden: true,
	Long: `The util namespace is a hidden home for tested-code primitives that an agent
calls instead of hand-rolling dangerous mechanics itself — starting with a
guarded config-file merge-write. These are not intended for direct user
invocation.`,
})

func init() {
	rootCmd.AddCommand(utilCmd)
}
