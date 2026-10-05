package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

var cleanYes bool

// `ctxloom clean` obeys the same rule the session destroyers do: ABSENCE OF
// --yes MEANS REPORT ONLY, on a TTY or not, and the report says so out loud
// (see session_purge_cmd.go's header). There is deliberately no --dry-run — a
// second spelling of a contract this project already has would only invite the
// two to disagree.
//
// Why each survivor survives:
//   - Approvals and application records are LOCAL-ONLY: nothing rebuilds them,
//     so not even --yes takes them.
//   - lock.yaml is rebuildable but committed, so deleting it would dirty the
//     tree rather than free anything.
//   - A session that predates the liveness lock has no lock file, so its owner
//     can never be proven dead, and this sweep, which ranges over every session
//     at once, never reclaims it. Nothing but the lock tells such a session
//     from a live one, and a bulk sweep is the wrong place to gamble a live
//     session's scratch on a guess; a human clears one by naming it
//     (`session transcript purge <name> --even-if-live`, and the artifacts
//     counterpart).
//   - A session never compacted keeps its persistent members under
//     --include-persist: its transcript is its only record.
//
// clean is not uninstall: what it takes comes back on the next run. A run
// never writes the project's integration surfaces (hooks, statusline, MCP
// registration, generated command files); only `manage hooks install` or
// `profile materialize` do, so what `manage uninstall` removes stays gone.
var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove this project's regenerable cache, keeping everything a clone cannot restore",
	Long: `Removes .ctxloom/cache: the pulled bundle copies, the git clone cache, the
assembled context files and the refused-advance record. Each is rebuilt by a
command the report names.

It also reclaims the disposable parts of ended sessions under
~/.ctxloom/sessions/<session-name>/ (the per-session engine home, scratch/ and
the worktree checkouts under work/) once nothing there has changed for 30d,
or session_reap_age in ~/.ctxloom/config.yaml. --older-than overrides that for
one run, as an age (30d, 12w, 720h) or a date (2026-01-01). --include-persist
also takes transcripts, engine history, the spool, the package store and
logs, except from a session that was never compacted.

Never taken: authored content and profiles, approvals, lock.yaml, a session's
identity and its output dir, a session with an empty 'keep' file at the top
of its directory, a session that is running or whose liveness cannot be
proven, and a worktree with uncommitted work. The report says what it left.

Without --yes this only reports. 'ctxloom session sweep' applies the same
rules to one project's ended sessions; 'ctxloom manage uninstall' removes
ctxloom's hooks and generated files from the project.`,
	Example: `  ctxloom clean                       # report what would be reclaimed
  ctxloom clean --older-than 30d --yes`,
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
	rep := cleanReport{CleanResult: res}

	policy, err := sessionReapPolicy(time.Now())
	if err != nil {
		return err
	}
	sweep, err := operations.SweepSessions(cmd.Context(), nil, operations.SweepRequest{
		ReclaimCutoff: policy.Cutoff,
		ReclaimScope:  policy.Scope,
		AllProjects:   true,
		Apply:         policy.Apply,
		ReclaimOnly:   true,
	})
	if err != nil {
		return err
	}
	rep.Sessions = *sweep.Reclaim

	if err := emit(cmd, rep, func() error {
		return renderCleanPlan(cmd.OutOrStdout(), rep)
	}); err != nil {
		return err
	}
	if !cleanYes {
		return reportPlanOnly(cmd, "ctxloom clean --yes")
	}
	return nil
}

// cleanReport is `clean`'s payload: the cache plan plus the aged-session
// plan. The sweep always runs (the age is defaulted), so Sessions is always
// present.
type cleanReport struct {
	operations.CleanResult
	Sessions sessions.Report `json:"sessions"`
}

func renderCleanPlan(w io.Writer, rep cleanReport) error {
	out := errwriter.New(w)
	res := rep.CleanResult
	present := 0
	for _, t := range res.Targets {
		if t.Present {
			present++
		}
	}
	if present == 0 {
		out.Printf("Nothing to clean: this project's cache is already absent.\n")
		return renderSessionReclaim(out, rep.Sessions)
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
	return renderSessionReclaim(out, rep.Sessions)
}

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
