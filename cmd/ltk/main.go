// Command ltk is the llm-tool-killer CLI.
//
//	ltk evaluate            # the hook: read a payload on stdin, emit a decision
//	ltk manage install      # add the hook to the most relevant LLM config
//	ltk manage uninstall    # remove it
//
// The hook gates two kinds of agent action: shell commands (Bash/PowerShell
// tools, matched by command rules) and file edits (Edit/Write/MultiEdit/
// NotebookEdit tools, matched by `path_rules`). See https://ctxloom.dev/ltk/rules/.
package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/logboot"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

// Version is set at build time via ldflags (package main), e.g.
//
//	-X main.Version=v1.2.3
//
// It defaults to "dev"; the justfile stamps it from versionator.
var Version = "dev"

// newRootCmd assembles the ltk command tree. It is a factory rather than an
// inline root so the documentation generator can walk exactly the tree the
// binary runs (`just gen-docs`; see docs_gen.go).
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   progName,
		Short: "Gate an LLM agent's shell commands and file edits via a pre-tool hook",
		Long: `ltk is a pre-tool hook for LLM coding agents. It gates two kinds of action:

  • shell commands (Bash/PowerShell tools) — matched by command rules
    (rules:), which parse the command and match each argument against an
    anchored regular expression, across shells.
  • file edits (Edit/Write/MultiEdit/NotebookEdit tools) — matched by file
    rules (path_rules:), which match the target file path against globs.

File rules use full globs (*, ?, [..], {a,b}, and ** which spans
directories). A trailing slash means a whole directory subtree (vendor/ blocks
everything under vendor). The special pattern "@submodules" expands to every
path in .gitmodules, so one rule blocks edits inside all git submodules.

On a denial the agent is handed your message and suggested alternative so it can
retry the right way. See https://ctxloom.dev/ltk/rules/ for the full rule model.`,
		Version:      Version,
		SilenceUsage: true,
		// A machine-readable run carries machine-readable warnings too.
		PersistentPreRun: func(cmd *cobra.Command, _ []string) { cobrafmt.ApplyDiagnostics(cmd) },
	}
	cobrafmt.AddFlag(root)
	schemaver.BindWriteUpgrades(root.PersistentFlags())
	root.AddCommand(newEvaluateCmd(), newCheckCmd(), newManageCmd(), newVersionCmd(), newLoadoutCmd())
	registerDocsCmd(root)
	return root
}

func main() {
	// Before anything can log, so a stalled lock wait leaves a record on disk.
	// The sink is lazy: ltk runs before every agent shell command, and a run
	// that logs nothing must write nothing. Not verbose: a stderr tee on a hook
	// is exactly the output the sink exists to keep off that surface.
	flush := logboot.Install("ltk", false)

	// Execute reports a failure in the family's form ("ltk: <msg>", or an
	// envelope under an explicit structured --format) and returns the status;
	// it never exits, so the flush below still runs.
	code := cobrafmt.Execute(newRootCmd(), progName, os.Stderr)
	// Flushed as a plain statement before the exit; see logboot.Install.
	flush()
	os.Exit(code)
}
