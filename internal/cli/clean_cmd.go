package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var cleanYes bool

// `ctxloom clean` obeys the same rule the session destroyers do: ABSENCE OF
// --yes MEANS REPORT ONLY, on a TTY or not, and the report says so out loud
// (see session_purge_cmd.go's header). There is deliberately no --dry-run — a
// second spelling of a contract this project already has would only invite the
// two to disagree.
var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove this project's regenerable cache, keeping everything a clone cannot restore",
	Long: `Removes .ctxloom/cache — the pulled bundle copies, the git clone cache,
the assembled context files and the refused-advance record. Every one of them
is rebuilt by a command this report names, so the only cost is the time to
re-run it.

Nothing else is touched. Your authored content and profiles are committed and
a clone has them. Your session records, approvals and application records are
LOCAL-ONLY — nothing rebuilds them, so clean never takes them, and neither
does --yes. lock.yaml survives too: it is rebuildable but committed, so
deleting it would dirty your tree rather than free anything.

Without --yes this only reports; nothing on disk changes.

clean is not uninstall. What it takes comes back on your next run, because
that is what regenerable means. To remove ctxloom's integration with this
project — its hooks, statusline, MCP registration and generated command
files — and have it stay removed, use 'ctxloom manage uninstall'.`,
	Args: cobra.NoArgs,
	RunE: runClean,
}

func runClean(cmd *cobra.Command, _ []string) error {
	// The project root comes from projectroot, NOT from config.
	//
	// The cache is what you reach for when the project is broken, so clean
	// must not need the project to be loadable in order to find it. Config
	// resolution is fault-tolerant today and would very likely have answered
	// too — but "very likely" is the wrong dependency for the one command
	// whose job is to work on a project that does not. projectroot answers
	// from CTXLOOM_ROOT or the git boundary and parses nothing.
	appDir := filepath.Join(projectroot.WorkDir(), paths.AppDirName)
	home, err := os.UserHomeDir()
	if err != nil {
		// Only RootHome entries need it, and no cache entry is one today; an
		// unresolvable home must not stop a project-scoped clean.
		home = ""
	}

	res, err := operations.CleanCache(appDir, home, cleanYes)
	if err != nil {
		return err
	}
	if err := emit(cmd, res, func() error {
		return renderCleanPlan(cmd.OutOrStdout(), res)
	}); err != nil {
		return err
	}
	if !cleanYes {
		return reportPlanOnly(cmd, "ctxloom clean --yes")
	}
	return nil
}

func renderCleanPlan(w io.Writer, res CleanResultAlias) error {
	out := iox.NewErrWriter(w)
	present := 0
	for _, t := range res.Targets {
		if t.Present {
			present++
		}
	}
	if present == 0 {
		out.Printf("Nothing to clean: this project's cache is already absent.\n")
		return out.Err()
	}

	verb := "would remove"
	if res.Applied {
		verb = "removed"
	}
	out.Printf("ctxloom %s %s of regenerable cache:\n\n", verb, humanBytes(res.Bytes))
	for _, t := range res.Targets {
		if !t.Present {
			continue
		}
		// The rebuild command is printed per path, not once in a footer: they
		// differ, and a caller who cleans is owed the specific command that
		// undoes what they just did to THAT path.
		out.Printf("  %-42s %10s   rebuild: %s\n", t.Rel, humanBytes(t.Bytes), t.Rebuild)
	}
	out.Printf("\n")
	return out.Err()
}

// CleanResultAlias keeps renderCleanPlan's signature readable without this
// package re-declaring the operation's result shape.
type CleanResultAlias = operations.CleanResult

func init() {
	cleanCmd.Flags().BoolVar(&cleanYes, "yes", false, "apply exactly the plan this reports")
	rootCmd.AddCommand(cleanCmd)
}

// humanBytes renders a size for a human reading a removal plan. Written here
// rather than taken as a dependency: it is eight lines, and the alternative
// puts a module in go.mod for one call site.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
