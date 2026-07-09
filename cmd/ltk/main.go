// Command ltk is the llm-tool-killer CLI.
//
//	ltk evaluate            # the hook: read a payload on stdin, emit a decision
//	ltk manage install      # add the hook to the most relevant LLM config
//	ltk manage uninstall    # remove it
//
// The hook gates two kinds of agent action: shell commands (Bash/PowerShell
// tools, matched by command rules) and file edits (Edit/Write/MultiEdit/
// NotebookEdit tools, matched by `match.path` rules). See docs/RULES.md.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via ldflags (package main), e.g.
//
//	-X main.Version=v1.2.3
//
// It defaults to "dev"; the justfile stamps it from versionator.
var Version = "dev"

func main() {
	root := &cobra.Command{
		Use:   progName,
		Short: "Gate an LLM agent's shell commands and file edits via a pre-tool hook",
		Long: `ltk is a pre-tool hook for LLM coding agents. It gates two kinds of action:

  • shell commands (Bash/PowerShell tools) — matched by command rules, which
    parse the command and match program/args across shells.
  • file edits (Edit/Write/MultiEdit/NotebookEdit tools) — matched by file
    rules (match.path), which match the target file path against globs.

File (match.path) rules use full globs (*, ?, [..], {a,b}, and ** which spans
directories). A trailing slash means a whole directory subtree (vendor/ blocks
everything under vendor). The special pattern "@submodules" expands to every
path in .gitmodules, so one rule blocks edits inside all git submodules.

On a denial the agent is handed your message and suggested alternative so it can
retry the right way. See docs/RULES.md for the full rule model.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newEvaluateCmd(), newCheckCmd(), newManageCmd(), newVersionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, progName+":", err)
		os.Exit(1)
	}
}
