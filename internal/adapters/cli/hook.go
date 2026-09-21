package cli

import (
	"github.com/spf13/cobra"
)

var hookCmd = groupNode(&cobra.Command{
	Use:    "hook",
	Short:  "Machine callbacks invoked by generated harness files",
	Hidden: true, // Internal command - called by AI tools, not directly by users
	Long: `The hook namespace is the single home for ctxloom's machine callbacks: the
commands an engine's generated hook configuration invokes during a session,
each on the lifecycle event it is declared for, plus the statusline HUD.
These are not intended for direct user invocation.`,
})

func init() {
	rootCmd.AddCommand(hookCmd)
}
