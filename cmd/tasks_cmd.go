package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
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
	tasksListAll      bool
)

var tasksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tasks, optionally filtered by status or term",
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := operations.ListTasks(taskContext(), tasksListStatuses, tasksListTerm, tasksListAll, false)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		if tasksListJSON {
			return writeJSON(cmd.OutOrStdout(), res.Tasks)
		}
		return renderTaskTable(cmd.OutOrStdout(), res.Tasks)
	},
}

var (
	tasksAddStatus  string
	tasksAddTrigger string
)

var tasksAddCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Add a new task",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := strings.Join(args, " ")
		res, err := operations.AddTask(taskContext(), text, tasksAddStatus, tasksAddTrigger)
		if err != nil {
			return err
		}
		warnTask(res.Warning)
		task := res.Task
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	},
}

var tasksStatusTrigger string

var tasksStatusCmd = &cobra.Command{
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
		task := res.Task
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	},
}

var tasksEditCmd = &cobra.Command{
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
		task := res.Task
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	},
}

var tasksSummaryCmd = &cobra.Command{
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

var tasksRunNoStart bool

var tasksRunCmd = &cobra.Command{
	Use:   "run [task-harp-id]",
	Short: "Browse a task and launch an agent on it",
	Long: `Browse the project's open tasks and launch an agent on the chosen one.

The selected task is marked "In Progress" in the project task log under a
brand-new session that continues the task's originating session. The agent
starts with the task text as its prompt.

With a task harp id argument the picker is skipped and that task is launched
directly. In a non-interactive shell a task harp id is required.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// All of the project's tasks live in one append-only log now (ADR
		// 0025); each carries the harp of the session that originated it.
		res, err := operations.ListTasks(taskContext(), nil, "", false, false)
		if err != nil {
			return fmt.Errorf("list tasks: %w", err)
		}
		warnTask(res.Warning)
		located := make([]tasks.Located, 0, len(res.Tasks))
		for _, tk := range res.Tasks {
			located = append(located, tasks.Located{Task: tk, OriginHarp: tk.OriginSession})
		}

		var chosen tasks.Located
		if len(args) == 1 {
			match, ok := findLocated(located, args[0])
			if !ok {
				return fmt.Errorf("task not found: %s", args[0])
			}
			chosen = match
		} else {
			if !isInteractiveTerminal() {
				return fmt.Errorf("no task harp id given and not a terminal; pass a task harp id (see `ctxloom tasks list`)")
			}
			pick, ok := pickTask(os.Stderr, os.Stdin, located)
			if !ok {
				fmt.Fprintln(os.Stderr, "ctxloom: cancelled")
				return nil
			}
			chosen = pick
		}

		return launchTaskAgent(chosen, tasksRunNoStart)
	},
}

// taskRunWorkDir resolves the project root the same way `ctxloom run` does:
// the CTXLOOM_ROOT override when valid, else the git root when in a repo,
// otherwise the current directory.
func taskRunWorkDir() string {
	return projectroot.WorkDir()
}

// findLocated returns the task with the given harp id, searching all statuses.
func findLocated(located []tasks.Located, harpID string) (tasks.Located, bool) {
	for _, l := range located {
		if l.HarpID == harpID {
			return l, true
		}
	}
	return tasks.Located{}, false
}

// launchTaskAgent shells out to `ctxloom run` to spin the chosen task into its
// own session. We re-invoke ourselves (rather than calling run inline) so the
// new session's harp — minted inside run by AssignHarp — owns the seeding;
// --seed-task there marks the task In Progress in the project log. Continuation
// of the originating session is requested via --session <origin> (omitted for
// tasks with no recorded origin).
func launchTaskAgent(chosen tasks.Located, noStart bool) error {
	prompt := fmt.Sprintf("Work on this task (`%s`): %s", chosen.HarpID, chosen.Text)
	runArgs := []string{"run"}
	if chosen.OriginHarp != "" {
		runArgs = append(runArgs, "--session", chosen.OriginHarp)
	}
	runArgs = append(runArgs, "--seed-task", chosen.HarpID)
	if noStart {
		runArgs = append(runArgs, "--seed-status", tasks.StatusToDo)
	}
	runArgs = append(runArgs, "--prompt", prompt)

	c := execCommand(resolveSelfExecutable(), runArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// pickTask renders a line-buffered task picker (mirroring the plain
// stdin/stderr idiom of internal/sessions.Picker — no TUI dependency) and
// returns the chosen task. The default view shows only open work (To Do /
// In Progress); `a` toggles showing every status. Returns ok=false when the
// user quits, EOF is hit, or there is nothing to show.
func pickTask(out io.Writer, in io.Reader, all []tasks.Located) (tasks.Located, bool) {
	if len(all) == 0 {
		fmt.Fprintln(out, "(no tasks)")
		return tasks.Located{}, false
	}
	showAll := false
	scanner := bufio.NewScanner(in)
	for {
		view := filterLocated(all, showAll)
		renderTaskPicker(out, view, showAll)
		if !scanner.Scan() {
			return tasks.Located{}, false
		}
		switch line := strings.TrimSpace(scanner.Text()); line {
		case "", "q", "quit", "exit":
			return tasks.Located{}, false
		case "a", "all":
			showAll = !showAll
		default:
			n, err := strconv.Atoi(line)
			if err != nil || n < 1 || n > len(view) {
				fmt.Fprintln(out, "  (enter a row number, a to toggle all statuses, q to quit)")
				continue
			}
			return view[n-1], true
		}
	}
}

// filterLocated keeps only open tasks (To Do / In Progress) unless showAll.
func filterLocated(all []tasks.Located, showAll bool) []tasks.Located {
	if showAll {
		return all
	}
	out := make([]tasks.Located, 0, len(all))
	for _, l := range all {
		if l.Status == tasks.StatusToDo || l.Status == tasks.StatusInProgress {
			out = append(out, l)
		}
	}
	return out
}

func renderTaskPicker(out io.Writer, view []tasks.Located, showAll bool) {
	if len(view) == 0 {
		fmt.Fprintln(out, "(no open tasks; press a to show all statuses, q to quit)")
	}
	for i, l := range view {
		origin := l.OriginHarp
		if origin == "" {
			origin = "(legacy)"
		}
		fmt.Fprintf(out, " [%d] %-12s %-50s · %s\n", i+1, l.Status, l.Text, origin)
	}
	scope := "open"
	if showAll {
		scope = "all"
	}
	fmt.Fprintf(out, "Choose [1-%d] launch · a toggle all (showing %s) · q quit\n> ", len(view), scope)
}

// tasksStampPlanCmd reads a Claude Code PostToolUse(Edit|Write) hook
// payload on stdin and, when the edited file matches the plan-file
// pattern, stamps the active session's harp name into the file's YAML
// frontmatter. No-op when CTXLOOM_SESSION_HARP is unset or the edited
// file isn't a plan file.
var tasksStampPlanCmd = &cobra.Command{
	Use:    "stamp-plan",
	Short:  "Stamp the active session's harp name into a plan file's frontmatter (internal — used by the PostFileEdit hook)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		harp := os.Getenv("CTXLOOM_SESSION_HARP")
		if harp == "" {
			// No active session — silent no-op so the hook is safe to
			// install before Phase 3's session naming ships.
			return nil
		}
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		path, err := parseEditPayload(raw)
		if err != nil || path == "" {
			return nil // malformed or no file_path — silent no-op
		}
		if !memory.IsPlanFile(path) {
			return nil
		}
		return tasks.StampPlanFile(path, harp)
	},
}

func init() {
	tasksListCmd.Flags().StringSliceVar(&tasksListStatuses, "status", nil, "filter by status (repeatable)")
	tasksListCmd.Flags().StringVar(&tasksListTerm, "term", "", "filter by case-insensitive substring of task text")
	tasksListCmd.Flags().BoolVar(&tasksListJSON, "json", false, "emit JSON instead of a table (for jq)")
	tasksListCmd.Flags().BoolVar(&tasksListAll, "all", false, "include completed (Done/Archived) tasks, hidden by default")

	tasksAddCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")
	tasksAddCmd.Flags().StringVar(&tasksAddTrigger, "trigger", "", "revive condition for a Deferred task (required when --status Deferred)")

	tasksStatusCmd.Flags().StringVar(&tasksStatusTrigger, "trigger", "", "revive condition when setting status to Deferred")

	tasksRunCmd.Flags().BoolVar(&tasksRunNoStart, "no-start", false, "Leave the task's status unchanged instead of marking it In Progress")

	tasksCmd.AddCommand(tasksListCmd, tasksAddCmd, tasksStatusCmd, tasksEditCmd, tasksSummaryCmd, tasksRunCmd, tasksStampPlanCmd)
	rootCmd.AddCommand(tasksCmd)
}

// parseEditPayload extracts tool_input.file_path from a Claude Code hook
// payload. Accepts both the wrapped (tool_input) and bare shapes for
// resilience.
func parseEditPayload(raw []byte) (string, error) {
	type input struct {
		FilePath string `json:"file_path"`
	}
	type wrapper struct {
		ToolInput *input `json:"tool_input"`
		input
	}
	var w wrapper
	if err := json.Unmarshal(raw, &w); err != nil {
		return "", err
	}
	if w.ToolInput != nil && w.ToolInput.FilePath != "" {
		return w.ToolInput.FilePath, nil
	}
	return w.FilePath, nil
}

// taskContext gathers the inputs operations needs to resolve the project task
// log (ADR 0019): the project root (git root, like `ctxloom run`), the
// project-id exported by `ctxloom run` pre-launch (empty for a bare CLI run, so
// operations resolves it live), and the active session harp stamped as task
// provenance.
func taskContext() operations.TaskContext {
	return operations.TaskContext{
		WorkDir:     taskRunWorkDir(),
		ProjectID:   os.Getenv("CTXLOOM_PROJECT_ID"),
		SessionHarp: os.Getenv("CTXLOOM_SESSION_HARP"),
	}
}

// warnTask surfaces a project-resolution notice (move/fork) returned by an
// operations task call.
func warnTask(warning string) {
	if warning != "" {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", warning)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func renderTaskTable(out io.Writer, list []tasks.Task) error {
	w := iox.NewErrWriter(out)
	if len(list) == 0 {
		w.Println("(no tasks)")
		return w.Err()
	}
	// Pad the harp-id column to the widest id in this list so the columns line
	// up regardless of id length (ids are never truncated — they're copy-paste
	// identifiers). The machine-readable view is --json; this table is for eyes.
	idWidth := 0
	for _, t := range list {
		if len(t.HarpID) > idWidth {
			idWidth = len(t.HarpID)
		}
	}
	for _, t := range list {
		check := " "
		if t.Checked {
			check = "x"
		}
		text := t.Text
		if t.Trigger != "" {
			text = fmt.Sprintf("%s  (trigger: %s)", text, t.Trigger)
		}
		w.Printf("[%s] %-*s  %-11s  %s\n", check, idWidth, t.HarpID, t.Status, text)
	}
	return w.Err()
}
