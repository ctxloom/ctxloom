package main

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
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
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := operations.ListTasksWithTagQuery(taskContext(), tasksListStatuses, tasksListTerm, tasksListTagQuery, tasksListAll, false)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
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

var (
	tasksAddStatus  string
	tasksAddTrigger string
	tasksAddTags    []string
)

var addCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Add a new task",
	Args:  cobra.MinimumNArgs(1),
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
applied before --remove, so a tag named in both ends up removed. Filter
"taskloom list --tag-query" using the resulting tags.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
	listCmd.Flags().StringVar(&tasksListTagQuery, "tag-query", "", `filter by postfix tag query, e.g. "urgent/release/and" (bare tag lists are an implicit AND)`)
	listCmd.Flags().BoolVar(&tasksListJSON, "json", false, "emit JSON instead of a table (for jq)")
	listCmd.Flags().BoolVar(&tasksListAll, "all", false, "include completed (Done/Archived) tasks, hidden by default")

	addCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")
	addCmd.Flags().StringVar(&tasksAddTrigger, "trigger", "", "revive condition for a Deferred task (required when --status Deferred)")
	addCmd.Flags().StringArrayVar(&tasksAddTags, "tag", nil, "flat tag to set at creation (repeatable)")

	statusCmd.Flags().StringVar(&tasksStatusTrigger, "trigger", "", "revive condition when setting status to Deferred")

	tagCmd.Flags().StringArrayVar(&tasksTagAdd, "add", nil, "tag to add (repeatable)")
	tagCmd.Flags().StringArrayVar(&tasksTagRemove, "remove", nil, "tag to remove (repeatable)")

	statusesCmd.Flags().BoolVar(&tasksStatusesJSON, "json", false, "emit JSON instead of a table (for jq)")

	rootCmd.AddCommand(listCmd, addCmd, statusCmd, editCmd, tagCmd, summaryCmd, statusesCmd)
}
