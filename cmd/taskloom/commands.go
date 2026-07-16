package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/pkg/tagquery"
)

var (
	tasksListStatuses []string
	tasksListTerm     string
	tasksListTagQuery string
	tasksListJSON     bool
	tasksListAll      bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks, optionally filtered by status, term, or tag query",
	Long: `List tasks, filtered by status, text term, and/or tag query.

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
  taskloom list --status "In Progress" --term docs`,
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := operations.ListTasksWithTagQuery(taskContext(), tasksListStatuses, tasksListTerm, tasksListTagQuery, tasksListAll, false)
		if err != nil {
			var perr *tagquery.ParseError
			if errors.As(err, &perr) {
				return fmt.Errorf("%w\nqueries are postfix: tags first, operator after — e.g. urgent/release/and, urgent/not (see 'taskloom list --help' for more)", err)
			}
			return err
		}
		warnTask(res.Warning)
		noteHiddenMatches(os.Stderr, res, tasksListTerm != "" || tasksListTagQuery != "")
		if tasksListJSON {
			return writeJSON(cmd.OutOrStdout(), res.Tasks)
		}
		// Name the resolved store: in multi-root workspaces (several .ctxloom
		// trees under one repo), which project a listing came from is the
		// first thing a confused reader needs to know.
		w := iox.NewErrWriter(cmd.OutOrStdout())
		if res.ProjectDir != "" {
			w.Printf("Project: %s (%s)\n\n", res.ProjectDir, res.ProjectID)
		} else {
			w.Printf("Project: %s\n\n", res.ProjectID)
		}
		if err := w.Err(); err != nil {
			return err
		}
		return renderTaskTable(cmd.OutOrStdout(), res.Tasks)
	},
}

// noteHiddenMatches tells the user (on stderr, so stdout stays parseable) when
// the default active-only view suppressed tasks their filter matched. Only a
// searching intent (--term / --tag-query) earns the note: a bare `list` is a
// "show my active work" view where hiding finished tasks is the whole point,
// but a query that matches 49 tasks and prints 11 with no trace is a silent
// truncation of an answer.
func noteHiddenMatches(w io.Writer, res *operations.TaskListResult, filtered bool) {
	hidden := res.HiddenCompleted + res.HiddenDeferred
	if !filtered || hidden == 0 {
		return
	}
	parts := make([]string, 0, 2)
	if res.HiddenCompleted > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", res.HiddenCompleted))
	}
	if res.HiddenDeferred > 0 {
		parts = append(parts, fmt.Sprintf("%d deferred", res.HiddenDeferred))
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
		res, err := operations.AddTaskWithTags(taskContext(), text, tasksAddStatus, tasksAddTrigger, tasksAddTags)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
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
		res, err := operations.SetTaskStatus(taskContext(), args[0], args[1], tasksStatusTrigger)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
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
		res, err := operations.EditTask(taskContext(), args[0], text)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
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
		res, err := operations.TagTask(taskContext(), args[0], tasksTagAdd, tasksTagRemove)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		noteTaskProject(res.ProjectID, res.ProjectDir)
		task := res.Task
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, strings.Join(task.Tags, ","))
		return w.Err()
	},
}

var tasksTagsJSON bool

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
		res, err := operations.ListTagCounts(taskContext())
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		if tasksTagsJSON {
			return writeJSON(cmd.OutOrStdout(), res.Tags)
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		if res.ProjectDir != "" {
			w.Printf("Project: %s (%s)\n\n", res.ProjectDir, res.ProjectID)
		} else {
			w.Printf("Project: %s\n\n", res.ProjectID)
		}
		if len(res.Tags) == 0 {
			w.Println("(no tags in use — apply one with `taskloom tag <harp-id> --add <tag>`)")
			return w.Err()
		}
		// Pad the tag column so the counts align; the counts are labeled so the
		// output is self-describing without a header row.
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
	},
}

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show per-status counts and active in-progress tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := operations.ListTasks(taskContext(), nil, "", false, true)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		sum := res.Summary
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
	},
}

var tasksStatusesJSON bool

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
		if tasksStatusesJSON {
			return writeJSON(cmd.OutOrStdout(), statuses)
		}
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
	},
}

func init() {
	listCmd.Flags().StringSliceVar(&tasksListStatuses, "status", nil, "filter by status (repeatable)")
	listCmd.Flags().StringVar(&tasksListTerm, "term", "", "filter by case-insensitive substring of task text")
	listCmd.Flags().StringVar(&tasksListTagQuery, "tag-query", "", `filter by postfix tag query, e.g. "urgent/release/and", "urgent/not" (see examples in --help; list tags with "taskloom tags")`)
	listCmd.Flags().BoolVar(&tasksListJSON, "json", false, "emit JSON instead of a table (for jq)")
	listCmd.Flags().BoolVar(&tasksListAll, "all", false, "include the tasks hidden by default: completed (Done/Archived) and Deferred")

	addCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")
	addCmd.Flags().StringVar(&tasksAddTrigger, "trigger", "", "revive condition for a Deferred task (required when --status Deferred)")
	addCmd.Flags().StringArrayVar(&tasksAddTags, "tag", nil, "flat tag to set at creation (repeatable)")

	statusCmd.Flags().StringVar(&tasksStatusTrigger, "trigger", "", "revive condition when setting status to Deferred")

	tagCmd.Flags().StringArrayVar(&tasksTagAdd, "add", nil, "tag to add (repeatable)")
	tagCmd.Flags().StringArrayVar(&tasksTagRemove, "remove", nil, "tag to remove (repeatable)")

	tagsCmd.Flags().BoolVar(&tasksTagsJSON, "json", false, "emit JSON instead of a table (for jq)")

	statusesCmd.Flags().BoolVar(&tasksStatusesJSON, "json", false, "emit JSON instead of a table (for jq)")

	rootCmd.AddCommand(listCmd, addCmd, statusCmd, editCmd, tagCmd, tagsCmd, summaryCmd, statusesCmd)
}
