package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/tagquery"
)

var (
	tasksListStatuses []string
	tasksListTerm     string
	tasksListTagQuery string
	tasksListAll      bool
	tasksListGlobal   bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks, optionally filtered by status, term, or tag query",
	Long: `List tasks, filtered by status, text term, and/or tag query.

By default a listing is scoped to the CURRENT project, resolved from the
working directory the same way ` + "`taskloom add`" + ` etc. do (--project, else
CTXLOOM_PROJECT_ID, else cwd). Pass --global to aggregate every project
instead. When no project can be resolved at all — not inside a git repo, no
CTXLOOM_ROOT override, and no prior task history at this exact path — the
listing falls back to --global on its own, with a notice on stderr saying
why, rather than minting a throwaway project identity for an arbitrary
directory.

By default only active tasks are shown: completed (Done/Archived) and
Deferred tasks are hidden. Pass --all to include them, or name a status
explicitly with --status (an explicit status filter is honored verbatim;
see "taskloom statuses" for the taxonomy). When a --term or --tag-query
filter also matches hidden tasks, a note on stderr says how many, so
matches never vanish silently.

Tag queries are postfix (RPN): a slash-separated path of tags and the
operators and/or/not, where each operator applies to the expression(s)
before it. A bare tag list with no operator is an implicit AND. Tags
match case-sensitively; operators are case-insensitive. Discover the
tags in use (with counts) via "taskloom tags".`,
	Example: `  # active tasks tagged both urgent AND release
  taskloom list --tag-query urgent/release/and

  # the same — a bare tag list is an implicit AND
  taskloom list --tag-query urgent/release

  # tagged urgent OR release
  taskloom list --tag-query urgent/release/or

  # active tasks NOT tagged urgent
  taskloom list --tag-query urgent/not

  # (urgent AND release) OR blocked — postfix composes left to right
  taskloom list --tag-query urgent/release/and/blocked/or

  # include completed and Deferred matches too
  taskloom list --tag-query release --all

  # In Progress tasks mentioning "docs"
  taskloom list --status "In Progress" --term docs

  # every project's tasks, not just the current one
  taskloom list --global`,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, err := cliemit.Resolve(cmd)
		if err != nil {
			return err
		}
		tc, err := taskContext()
		if err != nil {
			return err
		}
		return runListCmd(cmd.OutOrStdout(), os.Stderr, tc, listOptions{
			Statuses: tasksListStatuses,
			Term:     tasksListTerm,
			TagQuery: tasksListTagQuery,
			All:      tasksListAll,
			Global:   tasksListGlobal,
			Format:   format,
		})
	},
}

// runListCmd is listCmd's RunE body, factored out so it can be driven in
// tests without cobra machinery: out/errw are separate so a test can assert
// on each independently, matching the command's real stdout-stays-parseable
// / stderr-carries-diagnostics split. text renders the human table(s); any
// other format hands the raw rows to clifmt so the same data serializes to
// json/yaml/toml/markdown without a per-format branch here.
// listOptions bundles the resolved inputs for `taskloom list`. It replaces a
// long positional parameter list — several same-typed bools in a row are easy
// to transpose at a call site, and a named field says what each one means.
type listOptions struct {
	Statuses []string
	Term     string
	TagQuery string
	All      bool // include the default-hidden tasks: Done/Archived and Deferred
	Global   bool // aggregate across every project, not just the resolved one
	Format   clifmt.Format
}

func runListCmd(out, errw io.Writer, tc operations.TaskContext, opts listOptions) error {
	scope, err := resolveListScope(opts.Global, tc.ProjectID, tc.WorkDir)
	if err != nil {
		return err
	}
	filtered := opts.Term != "" || opts.TagQuery != ""

	if scope.Global {
		if scope.Notice != "" {
			clidiag.Fwarn(errw, "taskloom", "%s", scope.Notice)
		}
		gres, err := listAllProjects(opts.Statuses, opts.Term, opts.TagQuery, opts.All, tc.SessionHarp)
		if err != nil {
			return wrapTagQueryError(err)
		}
		noteHidden(errw, gres.HiddenCompleted, gres.HiddenDeferred, filtered)
		if opts.Format != clifmt.FormatText {
			return clifmt.Render(out, gres.Rows, opts.Format)
		}
		w := iox.NewErrWriter(out)
		w.Printf("Projects: %d (--global)\n\n", gres.ProjectCount)
		if err := w.Err(); err != nil {
			return err
		}
		return renderGlobalTaskTable(out, gres.Rows)
	}

	tc, err = requireHoming(tc)
	if err != nil {
		return err
	}
	res, err := operations.ListTasksWithTagQuery(tc, opts.Statuses, opts.Term, opts.TagQuery, opts.All, false)
	if err != nil {
		return wrapTagQueryError(err)
	}
	warnTask(res.Warning)
	noteHidden(errw, res.HiddenCompleted, res.HiddenDeferred, filtered)
	if opts.Format != clifmt.FormatText {
		return clifmt.Render(out, res.Tasks, opts.Format)
	}
	// Name the resolved store: in multi-root workspaces (several .ctxloom
	// trees under one repo), which project a listing came from is the
	// first thing a confused reader needs to know.
	w := iox.NewErrWriter(out)
	w.Printf("Project: %s\n\n", formatProjectLabel(res.ProjectDir, res.ProjectID))
	if err := w.Err(); err != nil {
		return err
	}
	return renderTaskTable(out, res.Tasks)
}

// wrapTagQueryError adds the postfix-grammar hint to a malformed --tag-query,
// shared by both the single-project and --global list paths.
func wrapTagQueryError(err error) error {
	var perr *tagquery.ParseError
	if errors.As(err, &perr) {
		return fmt.Errorf("%w\nqueries are postfix: tags first, operator after — e.g. urgent/release/and, urgent/not (see 'taskloom list --help' for more)", err)
	}
	return err
}

// renderGlobalTaskTable prints a --global (or no-project-fallback) listing as
// one table section per project, reusing renderTaskTable per section so the
// per-row formatting matches the single-project view exactly.
func renderGlobalTaskTable(out io.Writer, rows []taskRow) error {
	w := iox.NewErrWriter(out)
	if len(rows) == 0 {
		w.Println("(no tasks)")
		return w.Err()
	}
	start := 0
	for i := 1; i <= len(rows); i++ {
		if i < len(rows) && rows[i].ProjectID == rows[start].ProjectID {
			continue
		}
		group := rows[start:i]
		w.Printf("Project: %s\n", group[0].ProjectID)
		if err := w.Err(); err != nil {
			return err
		}
		plain := make([]tasks.Task, len(group))
		for j, r := range group {
			plain[j] = r.Task
		}
		if err := renderTaskTable(out, plain); err != nil {
			return err
		}
		w.Println("")
		start = i
	}
	return w.Err()
}

// noteHiddenMatches tells the user (on stderr, so stdout stays parseable) when
// the default active-only view suppressed tasks their filter matched. Only a
// searching intent (--term / --tag-query) earns the note: a bare `list` is a
// "show my active work" view where hiding finished tasks is the whole point,
// but a query that matches 49 tasks and prints 11 with no trace is a silent
// truncation of an answer.
func noteHiddenMatches(w io.Writer, res *operations.TaskListResult, filtered bool) {
	noteHidden(w, res.HiddenCompleted, res.HiddenDeferred, filtered)
}

// noteHidden is noteHiddenMatches' body, taking the counts directly rather
// than an operations.TaskListResult — the --global aggregation path sums
// counts across several projects' stores rather than getting them from one
// TaskListResult, so it needs the same hint off the raw numbers.
func noteHidden(w io.Writer, hiddenCompleted, hiddenDeferred int, filtered bool) {
	hidden := hiddenCompleted + hiddenDeferred
	if !filtered || hidden == 0 {
		return
	}
	parts := make([]string, 0, 2)
	if hiddenCompleted > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", hiddenCompleted))
	}
	if hiddenDeferred > 0 {
		parts = append(parts, fmt.Sprintf("%d deferred", hiddenDeferred))
	}
	fmt.Fprintf(w, "taskloom: %d more matching task(s) hidden by the default active-only view (%s) — add --all to include them\n",
		hidden, strings.Join(parts, ", "))
}

var (
	tasksAddStatus  string
	tasksAddTrigger string
	tasksAddTags    []string
)

var addCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Add a new task",
	Example: `  taskloom add "ship the release notes" --tag release --tag docs
  taskloom add "investigate flaky TestFoo" --status "In Progress"
  taskloom add "revisit caching" --status Deferred --trigger "the v2 API ships"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := strings.Join(args, " ")
		tc, err := taskContextSingle()
		if err != nil {
			return err
		}
		res, err := operations.AddTaskWithTags(tc, text, tasksAddStatus, tasksAddTrigger, tasksAddTags)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		return cliemit.Emit(cmd, task, func() error {
			w := iox.NewErrWriter(cmd.OutOrStdout())
			w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
			return w.Err()
		})
	},
}

var tasksStatusTrigger string

var statusCmd = &cobra.Command{
	Use:   "status <harp-id> <status>",
	Short: "Change the status of a task",
	Long: `Change the status of a task.

Use "Deferred" with --trigger to park a task on a named revive condition; the
task then hides from the default list until the trigger fires. A task already
carrying a trigger keeps it when re-deferred, so --trigger is optional then.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tc, err := taskContextSingle()
		if err != nil {
			return err
		}
		res, err := operations.SetTaskStatus(tc, args[0], args[1], tasksStatusTrigger)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		return cliemit.Emit(cmd, task, func() error {
			w := iox.NewErrWriter(cmd.OutOrStdout())
			w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
			return w.Err()
		})
	},
}

var editCmd = &cobra.Command{
	Use:   "edit <harp-id> <text>",
	Short: "Replace a task's text in place (full new text)",
	Long: `Replace a task's text, keyed by its harp ID.

The entire text is replaced with what you pass (not patched); the task's
status and any Deferred trigger are left unchanged.`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := strings.Join(args[1:], " ")
		tc, err := taskContextSingle()
		if err != nil {
			return err
		}
		res, err := operations.EditTask(tc, args[0], text)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		return cliemit.Emit(cmd, task, func() error {
			w := iox.NewErrWriter(cmd.OutOrStdout())
			w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
			return w.Err()
		})
	},
}

var (
	tasksTagAdd    []string
	tasksTagRemove []string
)

var tagCmd = &cobra.Command{
	Use:   "tag <harp-id>",
	Short: "Add and/or remove flat tags on a task",
	Long: `Add and/or remove flat tags on a task, keyed by its harp ID.

--add and --remove are each repeatable; at least one is required. --add is
applied before --remove, so a tag named in both ends up removed. See the
tags already in use with "taskloom tags"; filter tasks by tag with
"taskloom list --tag-query".`,
	Example: `  taskloom tag swift-amber-falcon --add urgent --add release
  taskloom tag swift-amber-falcon --remove urgent --add blocked`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(tasksTagAdd) == 0 && len(tasksTagRemove) == 0 {
			return fmt.Errorf("nothing to do: pass --add <tag> and/or --remove <tag>")
		}
		tc, err := taskContextSingle()
		if err != nil {
			return err
		}
		res, err := operations.TagTask(tc, args[0], tasksTagAdd, tasksTagRemove)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		return cliemit.Emit(cmd, task, func() error {
			w := iox.NewErrWriter(cmd.OutOrStdout())
			w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, strings.Join(task.Tags, ","))
			return w.Err()
		})
	},
}

var tagsCmd = &cobra.Command{
	Use:   "tags",
	Short: "List the tags in use, with per-tag task counts",
	Long: `List every tag currently in use across the project's tasks, with counts.

Counts cover ALL tasks: "active" is the number visible in the default list
view (not completed, not Deferred), "total" includes completed and Deferred
tasks too. A tag with 0 active but many total marks a finished workstream;
a tag with a single total next to a popular near-twin is probably a typo.

Apply tags with "taskloom tag" or "taskloom add --tag"; filter by them with
"taskloom list --tag-query".`,
	Example: `  taskloom tags
  taskloom tags --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tc, err := taskContextSingle()
		if err != nil {
			return err
		}
		res, err := operations.ListTagCounts(tc)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		return cliemit.Emit(cmd, res.Tags, func() error {
			w := iox.NewErrWriter(cmd.OutOrStdout())
			w.Printf("Project: %s\n\n", formatProjectLabel(res.ProjectDir, res.ProjectID))
			if len(res.Tags) == 0 {
				w.Println("(no tags in use — apply one with `taskloom tag <harp-id> --add <tag>`)")
				return w.Err()
			}
			// Pad the tag column so the counts align; the counts are labeled so
			// the output is self-describing without a header row.
			tagWidth := 0
			for _, t := range res.Tags {
				if len(t.Tag) > tagWidth {
					tagWidth = len(t.Tag)
				}
			}
			for _, t := range res.Tags {
				w.Printf("%-*s  %3d active  %3d total\n", tagWidth, t.Tag, t.Active, t.Total)
			}
			return w.Err()
		})
	},
}

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show per-status counts and active in-progress tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		tc, err := taskContextSingle()
		if err != nil {
			return err
		}
		res, err := operations.ListTasks(tc, nil, "", false, true)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		sum := res.Summary
		return cliemit.Emit(cmd, sum, func() error {
			// Stable order so output is diffable.
			keys := make([]string, 0, len(sum.Counts))
			for k := range sum.Counts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			w := iox.NewErrWriter(cmd.OutOrStdout())
			for _, k := range keys {
				w.Printf("%s\t%d\n", k, sum.Counts[k])
			}
			if len(sum.InProgress) > 0 {
				w.Printf("\nIn-progress: %s\n", strings.Join(sum.InProgress, ", "))
			}
			return w.Err()
		})
	},
}

var statusesCmd = &cobra.Command{
	Use:   "statuses",
	Short: "List the task status taxonomy (name, order, terminal, requires_trigger)",
	Long: `List the canonical task statuses in display order, with metadata.

Lets a GUI render status groups and pickers from the source of truth instead of
hardcoding the status set. "terminal" marks completed statuses (Done/Archived);
"requires_trigger" marks statuses that need a revive condition (Deferred).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		statuses := tasks.Statuses()
		return cliemit.Emit(cmd, statuses, func() error {
			w := iox.NewErrWriter(cmd.OutOrStdout())
			for _, s := range statuses {
				flags := ""
				if s.Terminal {
					flags += "\tterminal"
				}
				if s.RequiresTrigger {
					flags += "\trequires-trigger"
				}
				w.Printf("%d\t%s%s\n", s.Order, s.Name, flags)
			}
			return w.Err()
		})
	},
}

func init() {
	listCmd.Flags().StringSliceVar(&tasksListStatuses, "status", nil, "filter by status (repeatable)")
	listCmd.Flags().StringVar(&tasksListTerm, "term", "", "filter by case-insensitive substring of task text")
	listCmd.Flags().StringVar(&tasksListTagQuery, "tag-query", "", `filter by postfix tag query, e.g. "urgent/release/and", "urgent/not" (see examples in --help; list tags with "taskloom tags")`)
	listCmd.Flags().Bool("json", false, "shorthand for --format json (for jq)")
	listCmd.Flags().BoolVar(&tasksListAll, "all", false, "include the tasks hidden by default: completed (Done/Archived) and Deferred")
	listCmd.Flags().BoolVar(&tasksListGlobal, "global", false, "aggregate tasks across every project instead of just the current one")

	addCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")
	addCmd.Flags().StringVar(&tasksAddTrigger, "trigger", "", "revive condition for a Deferred task (required when --status Deferred)")
	addCmd.Flags().StringArrayVar(&tasksAddTags, "tag", nil, "flat tag to set at creation (repeatable)")

	statusCmd.Flags().StringVar(&tasksStatusTrigger, "trigger", "", "revive condition when setting status to Deferred")

	tagCmd.Flags().StringArrayVar(&tasksTagAdd, "add", nil, "tag to add (repeatable)")
	tagCmd.Flags().StringArrayVar(&tasksTagRemove, "remove", nil, "tag to remove (repeatable)")

	tagsCmd.Flags().Bool("json", false, "shorthand for --format json (for jq)")

	statusesCmd.Flags().Bool("json", false, "shorthand for --format json (for jq)")

	rootCmd.AddCommand(listCmd, addCmd, statusCmd, editCmd, tagCmd, tagsCmd, summaryCmd, statusesCmd)
}
