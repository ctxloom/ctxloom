package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
)

var showCmd = &cobra.Command{
	Use:   "show <harp-id> [harp-id...]",
	Short: "Show one or more tasks' full detail",
	Long: `Show tasks in full — status, tags, trigger, and complete
(never-truncated) text. This is the full-text companion to ` + "`taskloom list`" + `,
which prints one-line summaries: copy harp ids from the list and pass them here
to read the whole tasks. Several ids may be given in one call; output follows
ARGUMENT ORDER, not store order, and the store is read once however many ids
are asked for.

Every id must resolve. When any is unknown the call FAILS and names every id
that was not found — a partial result that looks complete is exactly the silent
truncation this project's diagnostics exist to prevent — so nothing is printed
at all rather than the subset that happened to resolve.

--format json/yaml/toml/markdown follow the project's serialization rule: a
GROUP serializes as a LIST, a SINGLE value as an OBJECT. One id therefore
yields a bare object, so ` + "`jq -r '.text'`" + ` reads its body; two or more yield an
array, so ` + "`jq -r '.[].text'`" + ` reads theirs. The shape follows what was ASKED
FOR, not what happened to be found — a single id that resolves is always an
object, never a one-element list.

Ids resolve in the same scope ` + "`taskloom list`" + ` reads: the current project by
default, every privately-homed project with --global (or when no project can be
resolved at all, with a notice on stderr). A cross-project read heads each
block with the project the task came from, and an id held by more than one
project FAILS naming them rather than picking one.`,
	Example: `  taskloom show swift-amber-falcon
  taskloom show swift-amber-falcon brisk-copper-otter
  taskloom show swift-amber-falcon brisk-copper-otter --format json
  taskloom show swift-amber-falcon --global`,
	Args: cobra.MinimumNArgs(1),
	RunE: runShow,
}

// showGlobal is show's --global: resolve the ids across every privately-homed
// project instead of only the resolved one, exactly the scope `list --global`
// reads.
var showGlobal bool

func runShow(cmd *cobra.Command, args []string) error {
	tc, err := taskContext()
	if err != nil {
		return err
	}
	tc, err = resolveTagSchema(tc)
	if err != nil {
		return err
	}
	// The scope decision is listTasksScoped's, the same one `list` and `tags`
	// make, so the three reads can never disagree about which project a
	// working directory means or when no project is in play at all. All is
	// forced on: a harp id shown by `list --all` (Done/Archived/Deferred
	// included) must still resolve here.
	r, err := listTasksScoped(tc, listOptions{All: true, Global: showGlobal})
	if err != nil {
		return err
	}
	if r.Notice != "" {
		clidiag.Fwarn(cmd.ErrOrStderr(), progName, "%s", r.Notice)
	}
	selected, missing, ambiguous := selectRows(r.Rows, args)
	if len(missing) > 0 {
		return missingTasksError(missing)
	}
	if len(ambiguous) > 0 {
		return ambiguousTasksError(ambiguous)
	}
	if !r.Global {
		noteProjectNewlyMinted(cmd.ErrOrStderr(), r.ProjectID, r.ProjectNewlyMinted)
		noteTaskProject(r.ProjectDir, r.ProjectID)
	}
	cfg := hideConfigFor(r.TC)
	// A GROUP serializes as a list and a SINGLE value as an object. The choice
	// keys off how many ids were ASKED FOR, not how many were found: every id
	// must resolve or the call already failed above, so the two counts agree —
	// but keying on the request is what makes the shape predictable from the
	// command line alone, without knowing the store's contents.
	//
	// A repeated id (`show a a`) is two ids asked for, so it stays a list, the
	// same way selectRows honors it twice.
	var payload any = selected
	if len(args) == 1 {
		payload = selected[0]
	}
	return cliemit.Emit(cmd, payload, func() error {
		return renderTaskDetails(cmd.OutOrStdout(), selected, r.Global, cfg)
	})
}

// ambiguousHarp is a requested harp id found in more than one project's store.
// Harp ids are unique within one project's log, not across projects, so only a
// cross-project read can produce one.
type ambiguousHarp struct {
	HarpID     string
	ProjectIDs []string
}

// selectRows resolves harpIDs against all in ARGUMENT ORDER, returning the
// matched rows and — separately — every id that matched nothing and every id
// that matched in more than one project. It resolves the whole request before
// reporting, so a caller naming three unknown ids learns all three from one
// run instead of one per re-invocation. A repeated id is honored as typed
// (selected twice); de-duplicating it would hand back fewer records than ids
// asked for, which is the partial-result shape this command refuses
// everywhere else.
func selectRows(all []taskRow, harpIDs []string) (selected []taskRow, missing []string, ambiguous []ambiguousHarp) {
	selected = make([]taskRow, 0, len(harpIDs))
	for _, id := range harpIDs {
		var matches []taskRow
		for _, r := range all {
			if r.HarpID == id {
				matches = append(matches, r)
			}
		}
		switch len(matches) {
		case 0:
			missing = append(missing, id)
		case 1:
			selected = append(selected, matches[0])
		default:
			projects := make([]string, len(matches))
			for i, m := range matches {
				projects[i] = m.ProjectID
			}
			ambiguous = append(ambiguous, ambiguousHarp{HarpID: id, ProjectIDs: projects})
		}
	}
	return selected, missing, ambiguous
}

// missingTasksError is the loud failure for ids that resolved to nothing,
// naming every one of them. Singular/plural wording is chosen from the count
// so the one-id case reads exactly as it always has.
func missingTasksError(missing []string) error {
	quoted := make([]string, len(missing))
	for i, id := range missing {
		quoted[i] = strconv.Quote(id)
	}
	if len(missing) == 1 {
		return fmt.Errorf("no task with harp id %s (see `taskloom list`)", quoted[0])
	}
	return fmt.Errorf("no tasks with harp ids %s (see `taskloom list`)", strings.Join(quoted, ", "))
}

// ambiguousTasksError is the loud failure for ids held by several projects,
// naming each id's projects and the flag that picks one: showing whichever
// project happened to sort first would be a confident answer about the wrong
// task.
func ambiguousTasksError(ambiguous []ambiguousHarp) error {
	parts := make([]string, len(ambiguous))
	for i, a := range ambiguous {
		parts[i] = fmt.Sprintf("%s in %s", strconv.Quote(a.HarpID), strings.Join(a.ProjectIDs, ", "))
	}
	return fmt.Errorf("harp id held by more than one project (%s); pass --project <id> instead of --global to choose one", strings.Join(parts, "; "))
}

// renderTaskDetails prints each row's full human view in the order given,
// blank-line separated so adjacent detail blocks read as distinct tasks
// rather than one run-on body — renderTaskDetail's last line is the task
// text, which would otherwise sit flush against the next block's header.
// When the rows span projects, each block is headed by the project it came
// from; a single-project read names its project once, on stderr.
func renderTaskDetails(out io.Writer, rows []taskRow, global bool, cfg tagma.HideConfig) error {
	for i, r := range rows {
		if i > 0 {
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
		}
		if global {
			if _, err := fmt.Fprintf(out, "project: %s\n", formatProjectLabel(r.ProjectDir, r.ProjectID)); err != nil {
				return err
			}
		}
		if err := renderTaskDetail(out, r.Task, cfg); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	showCmd.Flags().BoolVar(&showGlobal, "global", false, "resolve the ids across every privately-homed project instead of just the current one (repo-homed projects are never included -- see \"taskloom list --help\")")
	rootCmd.AddCommand(showCmd)
}

// renderTaskDetail prints one task's full human view: a header line (harp id +
// status), its tags and trigger if present, then the complete text — the
// detail `taskloom list` deliberately summarizes into a single line. cfg is
// applied to t's tags via visibleTags before printing — see hideConfigFor.
func renderTaskDetail(out io.Writer, t tasks.Task, cfg tagma.HideConfig) error {
	w := errwriter.New(out)
	check := " "
	if t.Checked {
		check = "x"
	}
	w.Printf("[%s] %s  %s\n", check, t.HarpID, t.Status)
	if visible := visibleTags(t.Tags, cfg); len(visible) > 0 {
		w.Printf("    tags: %s\n", strings.Join(visible, ", "))
	}
	if t.Trigger != "" {
		w.Printf("    trigger: %s\n", t.Trigger)
	}
	w.Println("")
	w.Println(t.Text)
	return w.Err()
}
