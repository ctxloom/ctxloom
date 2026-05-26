package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/tasks"
)

var tasksCmd = &cobra.Command{
	Use:   "tasks",
	Short: "Manage the project task store (.ctxloom/tasks.md)",
	Long: `Read and modify the per-project task store. Tasks are keyed by harp IDs
(see ` + "`ctxloom harp`" + `) and persisted as flesler-style markdown at
.ctxloom/tasks.md.

Tasks can also be managed via the MCP tools (task_list, task_add,
task_set_status) when running inside an MCP-aware backend.`,
}

var (
	tasksListStatuses []string
	tasksListTerm     string
	tasksListJSON     bool
)

var tasksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks, optionally filtered by status or term",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openCLITaskStore()
		if err != nil {
			return err
		}
		list, err := store.List(tasksListStatuses, tasksListTerm)
		if err != nil {
			return err
		}
		if tasksListJSON {
			return writeJSON(cmd.OutOrStdout(), list)
		}
		return renderTaskTable(cmd.OutOrStdout(), list)
	},
}

var tasksAddStatus string

var tasksAddCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Add a new task",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openCLITaskStore()
		if err != nil {
			return err
		}
		text := strings.Join(args, " ")
		task, err := store.Add(text, tasksAddStatus)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return nil
	},
}

var tasksStatusCmd = &cobra.Command{
	Use:   "status <harp-id> <status>",
	Short: "Change the status of a task",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openCLITaskStore()
		if err != nil {
			return err
		}
		task, err := store.SetStatus(args[0], args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return nil
	},
}

var tasksSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show per-status counts and active in-progress tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openCLITaskStore()
		if err != nil {
			return err
		}
		sum, err := store.Summarize()
		if err != nil {
			return err
		}
		// Stable order so output is diffable.
		keys := make([]string, 0, len(sum.Counts))
		for k := range sum.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d\n", k, sum.Counts[k])
		}
		if len(sum.InProgress) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "\nIn-progress: %s\n", strings.Join(sum.InProgress, ", "))
		}
		return nil
	},
}

// tasksCaptureCmd consumes a Claude Code PostToolUse(TodoWrite) hook
// payload on stdin and reconciles it against the project store. Wiring
// into the actual hook lands with PR C (the ctxloom-default-tasks bundle);
// this subcommand is the entry point that the bundle's shell script will
// pipe to.
var tasksCaptureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Reconcile a TodoWrite snapshot from stdin (internal — used by the auto-capture hook)",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openCLITaskStore()
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		items, err := parseTodoWritePayload(raw)
		if err != nil {
			return fmt.Errorf("parse payload: %w", err)
		}
		diff, err := store.Reconcile(items)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "added=%d updated=%d archived=%d\n",
			len(diff.Added), len(diff.Updated), len(diff.Archived))
		return nil
	},
}

func init() {
	tasksListCmd.Flags().StringSliceVar(&tasksListStatuses, "status", nil, "filter by status (repeatable)")
	tasksListCmd.Flags().StringVar(&tasksListTerm, "term", "", "filter by case-insensitive substring of task text")
	tasksListCmd.Flags().BoolVar(&tasksListJSON, "json", false, "emit JSON instead of a table")

	tasksAddCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")

	tasksCmd.AddCommand(tasksListCmd, tasksAddCmd, tasksStatusCmd, tasksSummaryCmd, tasksCaptureCmd)
	rootCmd.AddCommand(tasksCmd)
}

func openCLITaskStore() (*tasks.Store, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	return tasks.Open(wd)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func renderTaskTable(w io.Writer, list []tasks.Task) error {
	if len(list) == 0 {
		fmt.Fprintln(w, "(no tasks)")
		return nil
	}
	for _, t := range list {
		check := " "
		if t.Checked {
			check = "x"
		}
		fmt.Fprintf(w, "[%s] %-22s  %-12s  %s\n", check, t.HarpID, t.Status, t.Text)
	}
	return nil
}

// todoEntry mirrors one element of Claude Code's TodoWrite tool_input.todos
// array. `text` is a tolerated alias used by some clients.
type todoEntry struct {
	Content string `json:"content"`
	Text    string `json:"text"`
	Status  string `json:"status"`
}

// parseTodoWritePayload accepts the JSON shape Claude Code emits in a
// PostToolUse hook for the TodoWrite tool:
//
//	{ "tool_input": { "todos": [ {"content": "...", "status": "..."}, ... ] } }
//
// Also tolerated for ad-hoc use: `{ "todos": [...] }` and a bare `[...]` array.
// Empty payload is valid — it means "no active todos"; reconcile will then
// archive everything currently in the store.
func parseTodoWritePayload(raw []byte) ([]tasks.SnapshotItem, error) {
	type wrapper struct {
		Todos     []todoEntry `json:"todos"`
		ToolInput *struct {
			Todos []todoEntry `json:"todos"`
		} `json:"tool_input"`
	}
	var w wrapper
	if err := json.Unmarshal(raw, &w); err == nil {
		switch {
		case len(w.Todos) > 0:
			return todosToItems(w.Todos), nil
		case w.ToolInput != nil && len(w.ToolInput.Todos) > 0:
			return todosToItems(w.ToolInput.Todos), nil
		}
	}
	var arr []todoEntry
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return todosToItems(arr), nil
	}
	return nil, nil
}

func todosToItems(todos []todoEntry) []tasks.SnapshotItem {
	items := make([]tasks.SnapshotItem, 0, len(todos))
	for _, td := range todos {
		text := td.Content
		if text == "" {
			text = td.Text
		}
		if text == "" {
			continue
		}
		items = append(items, tasks.SnapshotItem{
			Text:   text,
			Status: tasks.MapTodoStatus(td.Status),
		})
	}
	return items
}
