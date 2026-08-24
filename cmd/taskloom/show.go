package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
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

--format json/yaml/toml/markdown emit an ARRAY of structured tasks, always:
one id yields a one-element array, not a bare object, so a consumer never has
to branch on how many ids it asked for.`,
	Example: `  taskloom show swift-amber-falcon
  taskloom show swift-amber-falcon brisk-copper-otter
  taskloom show swift-amber-falcon brisk-copper-otter --format json`,
	Args: cobra.MinimumNArgs(1),
	RunE: runShow,
}

func runShow(cmd *cobra.Command, args []string) error {
	// Search every status: a harp id shown by `list --all` (Done/Archived
	// included) must still resolve here.
	tc, err := taskContextSingle()
	if err != nil {
		return err
	}
	res, err := operations.ListTasks(tc, operations.ListOptions{IncludeDone: true})
	if err != nil {
		return err
	}
	selected, missing := selectTasks(res.Tasks, args)
	if len(missing) > 0 {
		return missingTasksError(missing)
	}
	noteTaskProject(res.ProjectDir, res.ProjectID)
	cfg := hideConfigFor(tc)
	return cliemit.Emit(cmd, selected, func() error {
		return renderTaskDetails(cmd.OutOrStdout(), selected, cfg)
	})
}

// selectTasks resolves harpIDs against all in ARGUMENT ORDER, returning the
// matched tasks and — separately — every id that matched nothing. It resolves
// the whole request before reporting, so a caller naming three unknown ids
// learns all three from one run instead of one per re-invocation. A repeated
// id is honored as typed (selected twice); de-duplicating it would hand back
// fewer records than ids asked for, which is the partial-result shape this
// command refuses everywhere else.
func selectTasks(all []tasks.Task, harpIDs []string) (selected []tasks.Task, missing []string) {
	selected = make([]tasks.Task, 0, len(harpIDs))
	for _, id := range harpIDs {
		task, ok := findTask(all, id)
		if !ok {
			missing = append(missing, id)
			continue
		}
		selected = append(selected, task)
	}
	return selected, missing
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

// renderTaskDetails prints each task's full human view in the order given,
// blank-line separated so adjacent detail blocks read as distinct tasks
// rather than one run-on body — renderTaskDetail's last line is the task
// text, which would otherwise sit flush against the next block's header.
func renderTaskDetails(out io.Writer, list []tasks.Task, cfg tagma.HideConfig) error {
	for i, t := range list {
		if i > 0 {
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
		}
		if err := renderTaskDetail(out, t, cfg); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(showCmd)
}

// renderTaskDetail prints one task's full human view: a header line (harp id +
// status), its tags and trigger if present, then the complete text — the
// detail `taskloom list` deliberately summarizes into a single line. cfg is
// applied to t's tags via visibleTags before printing — see hideConfigFor.
func renderTaskDetail(out io.Writer, t tasks.Task, cfg tagma.HideConfig) error {
	w := iox.NewErrWriter(out)
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
