package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// sessionWorktreeRow is `session worktrees`' rendering projection — never the
// domain type, matching cli.SessionRow's own convention (see session_row.go).
type sessionWorktreeRow struct {
	Harp       string `json:"harp"                label:"Harp"      col:"HARP"`
	Worktree   string `json:"worktree"             label:"Worktree" col:"WORKTREE"`
	Path       string `json:"path"                 label:"Path"     col:"PATH"`
	OwnerPID   int    `json:"owner_pid,omitempty"  label:"Owner PID" col:"OWNER"`
	OwnerState string `json:"owner_state"          label:"Owner"    col:"OWNER STATE"`
	Verdict    string `json:"verdict"              label:"Verdict"  col:"VERDICT"`
	Reason     string `json:"reason,omitempty"     label:"Reason"   col:"REASON"`
}

// sessionWorktreeReport is `session worktrees`' --format json payload.
// Worktrees is normalized nil -> [] so JSON renders [] not null — the same
// reason loadSessionEntries does it.
type sessionWorktreeReport struct {
	Worktrees []sessionWorktreeRow `json:"worktrees"`
	Reaped    int                  `json:"reaped"`
	Spared    int                  `json:"spared"`
	Skipped   int                  `json:"skipped"`
	// Applied is true only when --reap --yes actually ran ReapWorktrees.
	// Every other invocation — bare, or --reap without --yes — is a plan:
	// nothing on disk changed, regardless of what Verdict says would happen.
	Applied bool `json:"applied"`
}

func newSessionWorktreeRow(c isolation.WorktreeCandidate) sessionWorktreeRow {
	return sessionWorktreeRow{
		Harp:       c.Harp,
		Worktree:   filepath.Base(c.Path),
		Path:       c.Path,
		OwnerPID:   c.OwnerPID,
		OwnerState: worktreeOwnerStateText(c),
		Verdict:    string(c.Verdict),
		Reason:     c.Reason,
	}
}

// worktreeOwnerStateText renders a WorktreeCandidate's owner probe as a short
// human word. OwnerPID == 0 means no sibling marker was found at all (never
// probed) rather than "pid 0 was probed and found dead" — pidalive.Probe is
// never even called in that case (see isolation.classifyOneWorktree), so
// OwnerState there is a meaningless zero value that must not be rendered as
// "dead".
func worktreeOwnerStateText(c isolation.WorktreeCandidate) string {
	if c.OwnerPID == 0 {
		return "no marker"
	}
	switch c.OwnerState {
	case pidalive.Dead:
		return "dead"
	case pidalive.Alive:
		return "alive"
	default:
		return "unsure"
	}
}

var (
	sessionWorktreesReap bool
	sessionWorktreesYes  bool
	sessionWorktreesHarp string
)

var sessionWorktreesCmd = &cobra.Command{
	Use:   "worktrees",
	Short: "List ctxloom-owned scratch worktrees, and (with --reap --yes) remove the ones it can prove are safe",
	Long: `Lists every "ctxloom-wt-*" checkout ctxloom itself created under
~/.ctxloom/sessions/<harp>/ephemeral/ — leftovers from a per-agent worktree
whose owning process crashed before it could clean up after itself — and, for
each one, the verdict isolation.ReapOrphanedWorktrees' safety rules would
reach: reapable (orphaned and clean), spared (orphaned but carrying real or
unknowable work), or skipped (owner alive, or its liveness can't be proven).

Read-only by default: bare "session worktrees" only reports, and so does
"session worktrees --reap" without --yes. Nothing is ever removed unless BOTH
--reap and --yes are given, on this exact invocation — matching "session
purge"'s rule: the absence of --yes always means report-only, never act,
regardless of whether the terminal is interactive.

A long-lived worktree ctxloom did not create (anything outside
~/.ctxloom/sessions/) is never listed or touched here — "ctxloom doctor"
reports on those, and only ever suggests the commands to remove them by hand.`,
	Args: cobra.NoArgs,
	RunE: runSessionWorktrees,
}

func init() {
	sessionWorktreesCmd.Flags().BoolVar(&sessionWorktreesReap, "reap", false,
		"Remove the worktrees this run can prove are safe (orphaned AND clean). Read-only without --yes.")
	sessionWorktreesCmd.Flags().BoolVarP(&sessionWorktreesYes, "yes", "y", false,
		"Act on exactly the plan this invocation printed. Ignored without --reap; no config key may pre-answer it.")
	sessionWorktreesCmd.Flags().StringVar(&sessionWorktreesHarp, "harp", "",
		"Restrict to one harp-named session's scratch worktrees (default: every harp)")
	sessionCmd.AddCommand(sessionWorktreesCmd)
}

// runSessionWorktrees is the cobra RunE for `ctxloom session worktrees`.
//
// It always classifies first (isolation.ClassifyOrphanedWorktrees), then —
// ONLY when both --reap and --yes were given — runs isolation.ReapWorktrees
// over exactly what was just classified, on this one invocation's own plan.
// The report renders through emit() regardless of outcome, so every --format
// still gets a real result; the exit-2 refusal (an action verb that changed
// nothing — docs/cli-ux-principles.md §7) is decided AFTER that render, by
// inspecting the final tally, and adds its own stderr explanation on top.
func runSessionWorktrees(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	harp := sessionWorktreesHarp
	if harp != "" {
		if err := verifyHarpDirExists(harp); err != nil {
			return err
		}
	}

	candidates, err := isolation.ClassifyOrphanedWorktrees(ctx, nil, harp)
	if err != nil {
		return fmt.Errorf("list scratch worktrees: %w", err)
	}

	applied := sessionWorktreesReap && sessionWorktreesYes
	if applied {
		candidates = isolation.ReapWorktrees(ctx, nil, candidates)
	}

	rep := sessionWorktreeReport{Worktrees: make([]sessionWorktreeRow, 0, len(candidates)), Applied: applied}
	for _, c := range candidates {
		rep.Worktrees = append(rep.Worktrees, newSessionWorktreeRow(c))
		switch c.Verdict {
		case isolation.VerdictReaped:
			rep.Reaped++
		case isolation.VerdictSpared:
			rep.Spared++
		case isolation.VerdictSkipped:
			rep.Skipped++
		}
	}

	if err := emit(cmd, rep, func() error {
		return renderSessionWorktrees(cmd.OutOrStdout(), rep)
	}); err != nil {
		return err
	}

	// An action verb that changed nothing refuses (docs/cli-ux-principles.md
	// §7): --reap --yes ran, and not one candidate actually moved to
	// VerdictReaped. Spared/skipped candidates are not a failure — the report
	// above already said why each one was left alone — but reporting "0" here
	// would make an unattended reap that found nothing to do indistinguishable
	// from one that refused to touch anything it could have.
	if applied && rep.Reaped == 0 {
		w := iox.NewErrWriter(cmd.ErrOrStderr())
		w.Printf("ctxloom: reap changed nothing (%d spared, %d skipped) — nothing was provably safe to remove\n", rep.Spared, rep.Skipped)
		if werr := w.Err(); werr != nil {
			return werr
		}
		return refusedExit()
	}
	return nil
}

// verifyHarpDirExists returns an ordinary error (exit 1) when --harp names a
// session that has no directory at all under ~/.ctxloom/sessions — the
// design's "ordinary error" case, distinct from a harp that exists but simply
// has no scratch worktrees (an empty, successful listing).
func verifyHarpDirExists(harp string) error {
	dir, err := paths.HarpDir(harp)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		if os.IsNotExist(statErr) {
			return fmt.Errorf("harp not found: %q", harp)
		}
		return fmt.Errorf("stat harp %q: %w", harp, statErr)
	}
	return nil
}

// renderSessionWorktrees is the text/markdown-free (clifmt handles markdown
// itself) human render: a table when there is anything to show, one summary
// line naming what happened (or would happen) after it.
func renderSessionWorktrees(w io.Writer, rep sessionWorktreeReport) error {
	ew := iox.NewErrWriter(w)
	if len(rep.Worktrees) == 0 {
		ew.Println("no ctxloom-owned scratch worktrees")
		return ew.Err()
	}
	if err := clifmt.Render(w, rep.Worktrees, clifmt.FormatText); err != nil {
		return err
	}
	if rep.Applied {
		ew.Printf("reaped %d, spared %d, skipped %d\n", rep.Reaped, rep.Spared, rep.Skipped)
	} else {
		ew.Printf("%d worktree(s) reviewed; %d spared, %d skipped (read-only — re-run with --reap --yes to remove what is provably safe)\n",
			len(rep.Worktrees), rep.Spared, rep.Skipped)
	}
	return ew.Err()
}
