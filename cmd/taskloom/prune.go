package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/agebound"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

var (
	pruneOlderThan string
	pruneStatuses  []string
	pruneTagQuery  string
	pruneYes       bool
)

// `taskloom prune` obeys the rule ctxloom's own destroyers do (see
// ctxloom clean): ABSENCE OF --yes MEANS REPORT ONLY, and the report says so
// out loud. There is deliberately no --dry-run — a second spelling of that
// contract would only invite the two to disagree.
var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Drop old finished tasks from the task log (reports only, without --yes)",
	Long: `Remove old finished tasks from this project's task log. The log is
append-only, so every task ever finished stays in it, and in every listing
that includes finished work, until something takes it out.

A task is selected when all of these hold:

  - its status is one of --status (default Done; repeatable, and the
    @open/@terminal classes work as they do for "taskloom list")
  - its LAST status change, the add that set its first status or the latest
    status move after it, is older than --older-than (default 7d; an age such
    as 30d, 12w or 720h, or a date such as 2026-01-01). Editing or tagging a
    task does not change its status, so neither makes it younger.
  - it matches --tag-query, when one is given (the postfix grammar of
    "taskloom list --tag-query")

Without --yes this only reports what it would prune and changes nothing.
With --yes every event of each selected task is removed from the log, and
every other task is left exactly as it was. The log is rewritten atomically
under its write lock, so a task added or moved meanwhile is never lost.

Pruning is not archiving: a pruned task is gone, not moved anywhere. To keep
a record of finished work, set it Archived and leave Archived out of
--status. A pruned harp id is no longer reserved, so it could, rarely, be
minted again for a new task. A surviving task that still names a pruned one,
by a relates:<harp> tag or the harp in its text or trigger, is listed in the
report; the reference itself is left as written.

A repo-homed log (homing: repo) is committed, so the rewrite is a change to
commit like any other. A union merge with a branch that still carries the
pruned events can bring them back; pruning again removes them.`,
	Example: `  # what the default prune would take: Done tasks whose status last changed
  # more than seven days ago
  taskloom prune

  # take them
  taskloom prune --yes

  # Done and Archived tasks untouched for a month, tagged release
  taskloom prune --older-than 30d --status Done --status Archived --tag-query release --yes

  # every finished status, by the @terminal class
  taskloom prune --status @terminal`,
	Args: cobra.NoArgs,
	RunE: runPrune,
}

func runPrune(cmd *cobra.Command, args []string) error {
	format, err := cobrafmt.Resolve(cmd)
	if err != nil {
		return err
	}
	tc, err := taskContextSingle()
	if err != nil {
		return err
	}
	return runPruneCmd(cmd.OutOrStdout(), os.Stderr, tc, pruneOptions{
		OlderThan: pruneOlderThan,
		Statuses:  pruneStatuses,
		TagQuery:  pruneTagQuery,
		Yes:       pruneYes,
		Format:    format,
	}, time.Now())
}

// pruneOptions bundles the resolved inputs for `taskloom prune`, as
// listOptions does for list.
type pruneOptions struct {
	OlderThan string   // as typed: an agebound age or date
	Statuses  []string // as typed: literal statuses and @-classes
	TagQuery  string
	Yes       bool
	Format    clifmt.Format
}

// pruneReport is prune's payload: the store and the selection it was asked
// for, beside what that selection took. Statuses are the EXPANDED ones, so a
// report on --status @terminal says which statuses it actually matched.
type pruneReport struct {
	ProjectID  string    `json:"project_id"`
	ProjectDir string    `json:"project_dir"`
	Statuses   []string  `json:"statuses"`
	Before     time.Time `json:"before"`
	TagQuery   string    `json:"tag_query"`
	tasks.PruneResult
}

// runPruneCmd is pruneCmd's RunE body, factored out so it can be driven in
// tests without cobra machinery. now is injected: the age bound is resolved
// against it, so a test pins which tasks are "older than 7d".
func runPruneCmd(out, errw io.Writer, tc operations.TaskContext, opts pruneOptions, now time.Time) error {
	before, err := agebound.Parse("--older-than", opts.OlderThan, now)
	if err != nil {
		return err
	}
	statuses, err := tasks.ExpandStatusClasses(opts.Statuses)
	if err != nil {
		return err
	}
	if len(statuses) == 0 {
		return errors.New("--status needs at least one status: prune never reads an empty status filter as every status")
	}
	res, err := operations.PruneTasks(tc, tasks.PruneRequest{
		Statuses: statuses,
		Before:   before,
		TagQuery: opts.TagQuery,
		Apply:    opts.Yes,
	})
	if err != nil {
		return wrapTagQueryError(err)
	}
	if res.Warning != "" {
		clidiag.Fwarn(errw, progName, "%s", res.Warning)
	}
	rep := pruneReport{
		ProjectID:  res.ProjectID,
		ProjectDir: res.ProjectDir,
		Statuses:   statuses,
		// In UTC, like every timestamp the log itself carries, rather than
		// in whatever zone the caller's clock happened to be read in.
		Before:      before.UTC(),
		TagQuery:    opts.TagQuery,
		PruneResult: res.PruneResult,
	}
	if err := clifmt.Render(out, rep, opts.Format, clifmt.WithWriter(clifmt.FormatText, func(w io.Writer) error {
		return renderPruneReport(w, rep)
	})); err != nil {
		return err
	}
	if !opts.Yes && rep.TasksPruned > 0 {
		w := errwriter.New(errw)
		w.Printf("taskloom removed nothing — this was a report, not a removal. To apply exactly this plan, run it again with --yes:\n  %s\n", pruneApplyCommand(opts))
		return w.Err()
	}
	return nil
}

// renderPruneReport prints prune's human view: what it selected by, each task
// it took (or would take) with the status change that put it past the bound,
// and every reference a survivor still holds to one of them.
func renderPruneReport(out io.Writer, rep pruneReport) error {
	w := errwriter.New(out)
	// No label means no project here yet; the warning on stderr says so,
	// and an empty "Project:" line would only look broken.
	if label := formatProjectLabel(rep.ProjectDir, rep.ProjectID); label != "" {
		w.Printf("Project: %s\n\n", label)
	}
	criteria := fmt.Sprintf("status %s, last status change before %s",
		strings.Join(rep.Statuses, " or "), rep.Before.UTC().Format("2006-01-02 15:04 MST"))
	if rep.TagQuery != "" {
		criteria += ", tag query " + rep.TagQuery
	}
	if rep.TasksPruned == 0 {
		w.Printf("Nothing to prune (%s).\n", criteria)
		return w.Err()
	}
	verb := "Would prune"
	if rep.Applied {
		verb = "Pruned"
	}
	w.Printf("%s %d task(s), %d event(s) — %s:\n\n", verb, rep.TasksPruned, rep.EventsRemoved, criteria)
	idWidth, statusWidth := 0, 0
	for _, p := range rep.Pruned {
		idWidth = max(idWidth, len(p.HarpID))
		statusWidth = max(statusWidth, len(p.Status))
	}
	for _, p := range rep.Pruned {
		w.Printf("  %-*s  %-*s  %s  %s\n", idWidth, p.HarpID, statusWidth, p.Status,
			p.LastStatusChange.UTC().Format(time.DateOnly), p.Headline)
	}
	w.Printf("\n%d task(s) remain.\n", rep.TasksRemaining)
	if len(rep.Dangling) > 0 {
		w.Printf("\nStill named by a surviving task (left as written):\n")
		for _, d := range rep.Dangling {
			w.Printf("  %s -> %s  (%s)\n", d.HarpID, d.Pruned, d.Via)
		}
	}
	return w.Err()
}

// pruneApplyCommand spells the --yes run of the selection opts made, so a
// report hands its reader the exact command that applies it.
func pruneApplyCommand(opts pruneOptions) string {
	words := []string{progName, "prune", "--older-than", shellWord(opts.OlderThan)}
	for _, s := range opts.Statuses {
		words = append(words, "--status", shellWord(s))
	}
	if opts.TagQuery != "" {
		words = append(words, "--tag-query", shellWord(opts.TagQuery))
	}
	return strings.Join(append(words, "--yes"), " ")
}

// shellWord quotes s for a POSIX shell when it holds anything beyond the
// characters every shell passes through literally: a status like "To Do" or a
// relational tag query like triage:level<=2 must survive being pasted back.
func shellWord(s string) string {
	plain := s != "" && strings.IndexFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("@%+,-./:=_", r)
	}) < 0
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func init() {
	pruneCmd.Flags().StringVar(&pruneOlderThan, "older-than", "7d",
		"prune only tasks whose last status change is older than this age (30d, 12w, 720h) or date (2026-01-01)")
	pruneCmd.Flags().StringSliceVar(&pruneStatuses, "status", []string{tasks.StatusDone},
		`the statuses that qualify a task for pruning (repeatable); also accepts the classes "@open" and "@terminal", as for list`)
	pruneCmd.Flags().StringVar(&pruneTagQuery, "tag-query", "",
		`prune only tasks matching this postfix tag query, e.g. "urgent/release/and" (see "taskloom list --help" for the grammar)`)
	pruneCmd.Flags().BoolVar(&pruneYes, "yes", false,
		"rewrite the log; without it prune only reports what it would remove")
	rootCmd.AddCommand(pruneCmd)
}
