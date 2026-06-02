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

	"github.com/ctxloom/ctxloom/internal/gitutil"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
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
		store, err := openSessionTaskStore()
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
		store, err := openSessionTaskStore()
		if err != nil {
			return err
		}
		text := strings.Join(args, " ")
		task, err := store.Add(text, tasksAddStatus)
		if err != nil {
			return err
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	},
}

var tasksStatusCmd = &cobra.Command{
	Use:   "status <harp-id> <status>",
	Short: "Change the status of a task",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openSessionTaskStore()
		if err != nil {
			return err
		}
		task, err := store.SetStatus(args[0], args[1])
		if err != nil {
			return err
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	},
}

var tasksSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show per-status counts and active in-progress tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openSessionTaskStore()
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
	Long: `Browse the project's open tasks (across all sessions plus the legacy
project store) and launch an agent on the chosen one.

The selected task is moved out of its origin session's store into a brand-new
session that continues the origin session, and is marked "In Progress" there.
The agent starts with the task text as its prompt.

With a task harp id argument the picker is skipped and that task is launched
directly. In a non-interactive shell a task harp id is required.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		workDir := taskRunWorkDir()

		// Candidate session harps for this project come from the session
		// index; each task is tagged with the harp store it currently lives
		// in. Both the index read and the sessions-root lookup are
		// best-effort — a failure just narrows the browse to the legacy store.
		var harps []string
		if mgr, err := sessions.Open(""); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: session index open failed: %v\n", err)
		} else if entries, err := mgr.ListForProject(workDir); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: listing sessions failed: %v\n", err)
		} else {
			for _, e := range entries {
				harps = append(harps, e.HarpName)
			}
		}
		var sessionsRoot string
		if root, err := paths.HomeSessionsDir(); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: cannot resolve sessions dir: %v\n", err)
		} else {
			sessionsRoot = root
		}

		located, err := tasks.CollectForSessions(sessionsRoot, workDir, harps, nil)
		if err != nil {
			return err
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
// the git root when in a repo, otherwise the current directory.
func taskRunWorkDir() string {
	if root, err := gitutil.FindRoot("."); err == nil {
		return root
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
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
// new session's harp — minted inside run by AssignHarp — owns the seeding; the
// --seed-task hook there moves the task from its origin store into the new one.
// Continuation is requested via --session <origin> (omitted for legacy-store
// tasks, which have no session to continue).
func launchTaskAgent(chosen tasks.Located, noStart bool) error {
	prompt := fmt.Sprintf("Work on this task (`%s`): %s", chosen.HarpID, chosen.Text)
	runArgs := []string{"run"}
	if chosen.OriginHarp != "" {
		runArgs = append(runArgs, "--session", chosen.OriginHarp, "--no-tasks")
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
	tasksListCmd.Flags().BoolVar(&tasksListJSON, "json", false, "emit JSON instead of a table")

	tasksAddCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")

	tasksRunCmd.Flags().BoolVar(&tasksRunNoStart, "no-start", false, "Leave the task's status unchanged instead of marking it In Progress")

	tasksCmd.AddCommand(tasksListCmd, tasksAddCmd, tasksStatusCmd, tasksSummaryCmd, tasksRunCmd, tasksStampPlanCmd)
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

// openSessionTaskStore resolves the active task store for the current
// session context. When CTXLOOM_SESSION_HARP is set the store is the
// harp-keyed ~/.ctxloom/sessions/<harp>/tasks.md (migrated on first use from
// the resumed-from session, or from the legacy project file for a fresh
// session); otherwise it falls back to the legacy <cwd>/.ctxloom/tasks.md.
// Shared by the CLI `tasks` subcommands, the MCP task tools, and the task
// summary resource so all three resolve the same store.
func openSessionTaskStore() (*tasks.Store, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	var sessionsRoot string
	if os.Getenv("CTXLOOM_SESSION_HARP") != "" {
		if root, err := paths.HomeSessionsDir(); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: cannot resolve sessions dir: %v\n", err)
		} else {
			sessionsRoot = root
		}
	}
	return tasks.OpenSession(tasks.SessionConfig{
		Harp:         os.Getenv("CTXLOOM_SESSION_HARP"),
		ResumedFrom:  os.Getenv("CTXLOOM_RESUMED_FROM"),
		RestoreTasks: resumeRestoresTasks(),
		SessionsRoot: sessionsRoot,
		ProjectDir:   wd,
	})
}

// resumeRestoresTasks reports whether a resume elected to carry tasks
// forward. Mirrors the convention used elsewhere: an empty parts list on a
// resume defaults to "session,tasks".
func resumeRestoresTasks() bool {
	if os.Getenv("CTXLOOM_RESUMED_FROM") == "" {
		return false
	}
	parts := os.Getenv("CTXLOOM_RESUMED_PARTS")
	if parts == "" {
		return true
	}
	for _, p := range strings.Split(parts, ",") {
		if strings.TrimSpace(p) == "tasks" {
			return true
		}
	}
	return false
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
	for _, t := range list {
		check := " "
		if t.Checked {
			check = "x"
		}
		w.Printf("[%s] %-22s  %-12s  %s\n", check, t.HarpID, t.Status, t.Text)
	}
	return w.Err()
}
