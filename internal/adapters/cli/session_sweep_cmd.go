package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var (
	sessionSweepOlderThan      string
	sessionSweepPurgeOlderThan string
	sessionSweepAllProjects    bool
	sessionSweepYes            bool
)

var sessionSweepCmd = &cobra.Command{
	Use:   "sweep",
	Short: "Tidy every ended session of this project by rule: remove what it can prove is safe, report the rest",
	Long: `Walks every session of this project (--all-projects: every session on this
machine) and decides each one by a fixed table, in order:

  running                      skipped whole
  liveness unprovable          skipped whole; the manual route is named
  carries a 'keep' marker      kept
  a scratch worktree holds     its clean worktrees are removed; the session is
  uncommitted work             spared, and the work stays where it is
  clean scratch worktrees      removed
  older than --older-than      its disposable members reclaimed (what
                               'ctxloom clean' reclaims)
  distilled, older than        purged: transcript and essence go, the files
  --purge-older-than           you wrote stay and are named
  never distilled              never purged; 'ctxloom session distill <harp>'
                               is named
  an internal one-shot, older  purged without a distill
  than --purge-older-than
  undelivered mail             spared from purge, and counted

Nothing is decided by a model: the same sessions give the same report.

--older-than defaults to session_reap_age (30d). Purging has NO default:
without --purge-older-than or session_purge_age the purge rows are reported
as held and never acted on. Each takes an offset (30d, 12w, 720h) or a date
(2026-01-01).

Without --yes this only reports; nothing changes. --yes acts on exactly the
plan it reports, re-checking each session under its lock before acting.`,
	Args: cobra.NoArgs,
	RunE: runSessionSweep,
}

func runSessionSweep(cmd *cobra.Command, _ []string) error {
	now := time.Now()
	reclaim, err := reclaimCutoff(sessionSweepOlderThan, now)
	if err != nil {
		return err
	}
	purge, err := purgeCutoff(sessionSweepPurgeOlderThan, now)
	if err != nil {
		return err
	}
	rep, err := operations.SweepSessions(cmd.Context(), nil, operations.SweepRequest{
		ProjectDir:    projectroot.WorkDir(),
		ReclaimCutoff: reclaim,
		PurgeCutoff:   purge,
		AllProjects:   sessionSweepAllProjects,
		Apply:         sessionSweepYes,
	})
	if err != nil {
		return err
	}
	if err := emit(cmd, rep, func() error { return renderSweepReport(cmd.OutOrStdout(), rep) }); err != nil {
		return err
	}
	if !sessionSweepYes {
		return reportPlanOnly(cmd, "ctxloom session sweep --yes")
	}
	if n := rep.Counts[operations.SweepFailed]; n > 0 {
		return reportRefusal(cmd, fmt.Sprintf("ctxloom: %d sweep action(s) failed; the report says which", n))
	}
	return nil
}

// purgeCutoff resolves the purge bound: --purge-older-than when given, else
// session_purge_age, else ZERO — there is no default, and a zero bound holds
// every purge row.
func purgeCutoff(flag string, now time.Time) (time.Time, error) {
	if flag != "" {
		return parseAgeBound("--purge-older-than", flag, now)
	}
	cfg, err := GetConfig()
	if err != nil {
		clidiag.Warn("ctxloom", "config could not be loaded (%v); no session_purge_age applies, so nothing is purged", err)
		return time.Time{}, nil
	}
	if age := cfg.SessionPurgeAge(); age != "" {
		return parseAgeBound("session_purge_age", age, now)
	}
	return time.Time{}, nil
}

func renderSweepReport(w io.Writer, rep operations.SweepReport) error {
	out := iox.NewErrWriter(w)
	purge := "not set (purges are held)"
	if !rep.PurgeCutoff.IsZero() {
		purge = rep.PurgeCutoff.Format(time.DateOnly)
	}
	out.Printf("Session sweep: reclaim before %s, purge before %s.\n\n", rep.ReclaimCutoff.Format(time.DateOnly), purge)
	for _, r := range rep.Rows {
		size := ""
		if r.Bytes > 0 {
			size = humanBytes(r.Bytes)
		}
		out.Printf("  %-28s %-15s %-8s %10s\n", r.Harp, r.Action, r.Verdict, size)
		if r.Reason != "" {
			out.Printf("  %-28s %s\n", "", r.Reason)
		}
		for _, wt := range r.Worktrees {
			out.Printf("  %-28s worktree %s\n", "", wt)
		}
		if r.Command != "" {
			out.Printf("  %-28s run: %s\n", "", r.Command)
		}
	}
	out.Printf("\n%d done, %d planned, %d held, %d left, %d failed; %d session(s) had nothing to do.\n",
		rep.Counts[operations.SweepDone], rep.Counts[operations.SweepPlanned], rep.Counts[operations.SweepHeld],
		rep.Counts[operations.SweepLeft], rep.Counts[operations.SweepFailed], rep.Untouched)
	return out.Err()
}

func init() {
	f := sessionSweepCmd.Flags()
	f.StringVar(&sessionSweepOlderThan, "older-than", "",
		"reclaim the disposable members of sessions last active before this age or date, overriding session_reap_age")
	f.StringVar(&sessionSweepPurgeOlderThan, "purge-older-than", "",
		"purge ended sessions last active before this age or date, overriding session_purge_age; without either nothing is purged")
	f.BoolVar(&sessionSweepAllProjects, "all-projects", false, "sweep every session on this machine, not only this project's")
	f.BoolVar(&sessionSweepYes, "yes", false, "act on exactly the plan this reports")
	sessionCmd.AddCommand(sessionSweepCmd)
}
