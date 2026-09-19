package cli

import (
	"github.com/spf13/cobra"
)

var hookCmd = groupNode(&cobra.Command{
	Use:    "hook",
	Short:  "Machine callbacks invoked by generated harness files",
	Hidden: true, // Internal command - called by AI tools, not directly by users
	Long: `The hook namespace is the single home for ctxloom's machine callbacks: the
commands that generated backend config files invoke during a session
(SessionStart context injection and harp binding, PostFileEdit plan stamping,
and the statusline HUD). These are not intended for direct user invocation.`,
})

func init() {
	rootCmd.AddCommand(hookCmd)
}
