package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// execCommand and lookPath are the exec seams for tests.
var (
	execCommand = exec.Command
	lookPath    = exec.LookPath
)

var tasksRunNoStart bool

var runCmd = &cobra.Command{
	Use:   "run [task-harp-id]",
	Short: "Browse a task and launch an agent on it (via ctxloom run)",
	Long: `Browse the project's open tasks and launch an agent on the chosen one.

The selected task is marked "In Progress" in the project task log under a
brand-new session that continues the task's originating session. The agent
starts with the task text as its prompt. Launching shells out to ` + "`ctxloom run`" + `,
so ctxloom must be on PATH.

With a task harp id argument the picker is skipped and that task is launched
directly. In a non-interactive shell a task harp id is required.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// All of the project's tasks live in one append-only log; each carries
		// the harp of the session that originated it. Fetch EVERY status
		// (includeDone=true): the picker hides completed tasks by default
		// itself (filterOpen), so its `a` toggle can reveal them, and a direct
		// harp-id launch of a Done/Archived task still resolves.
		tc, err := taskContext()
		if err != nil {
			return err
		}
		res, err := operations.ListTasks(tc, nil, "", true, false)
		if err != nil {
			return fmt.Errorf("list tasks: %w", err)
		}
		warnTask(res.Warning)

		var chosen tasks.Task
		if len(args) == 1 {
			match, ok := findTask(res.Tasks, args[0])
			if !ok {
				return fmt.Errorf("task not found: %s", args[0])
			}
			chosen = match
		} else {
			if !isInteractiveTerminal() {
				return fmt.Errorf("no task harp id given and not a terminal; pass a task harp id (see `taskloom list`)")
			}
			pick, ok := pickTask(os.Stderr, os.Stdin, res.Tasks)
			if !ok {
				fmt.Fprintln(os.Stderr, "taskloom: cancelled")
				return nil
			}
			chosen = pick
		}

		return launchTaskAgent(chosen, tasksRunNoStart)
	},
}

func init() {
	runCmd.Flags().BoolVar(&tasksRunNoStart, "no-start", false, "Reset the task to To Do instead of marking it In Progress")
	rootCmd.AddCommand(runCmd)
}

// findTask returns the task with the given harp id, searching all statuses.
func findTask(all []tasks.Task, harpID string) (tasks.Task, bool) {
	for _, t := range all {
		if t.HarpID == harpID {
			return t, true
		}
	}
	return tasks.Task{}, false
}

// launchTaskAgent shells out to `ctxloom run` to spin the chosen task into its
// own session. The new session's harp — minted inside ctxloom run — owns the
// seeding; --seed-task there marks the task In Progress in the project log.
// Continuation of the originating session is requested via --session <origin>
// (omitted for tasks with no recorded origin).
func launchTaskAgent(chosen tasks.Task, noStart bool) error {
	prompt := fmt.Sprintf("Work on this task (`%s`): %s", chosen.HarpID, chosen.Text)
	runArgs := []string{"run"}
	if chosen.OriginSession != "" {
		runArgs = append(runArgs, "--session", chosen.OriginSession)
	}
	runArgs = append(runArgs, "--seed-task", chosen.HarpID)
	if noStart {
		runArgs = append(runArgs, "--seed-status", tasks.StatusToDo)
	}
	runArgs = append(runArgs, "--prompt", prompt)

	// run is taskloom's only ctxloom-coupled subcommand; everything else works
	// standalone. Surface that boundary instead of a bare exec error.
	if _, err := lookPath("ctxloom"); err != nil {
		return fmt.Errorf("taskloom run requires ctxloom on PATH to launch the agent session (https://github.com/ctxloom/ctxloom); all other taskloom commands work without it")
	}

	c := execCommand("ctxloom", runArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// pickTask renders a line-buffered task picker (plain stdin/stderr, no TUI
// dependency) and returns the chosen task. The default view shows only open
// work (To Do / In Progress); `a` toggles showing every status. Returns
// ok=false when the user quits, EOF is hit, or there is nothing to show.
func pickTask(out io.Writer, in io.Reader, all []tasks.Task) (tasks.Task, bool) {
	if len(all) == 0 {
		fmt.Fprintln(out, "(no tasks)")
		return tasks.Task{}, false
	}
	showAll := false
	scanner := bufio.NewScanner(in)
	for {
		view := filterOpen(all, showAll)
		renderTaskPicker(out, view, showAll)
		if !scanner.Scan() {
			return tasks.Task{}, false
		}
		switch line := strings.TrimSpace(scanner.Text()); line {
		case "", "q", "quit", "exit":
			return tasks.Task{}, false
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

// filterOpen keeps only open tasks (To Do / In Progress) unless showAll.
func filterOpen(all []tasks.Task, showAll bool) []tasks.Task {
	if showAll {
		return all
	}
	out := make([]tasks.Task, 0, len(all))
	for _, t := range all {
		if t.Status == tasks.StatusToDo || t.Status == tasks.StatusInProgress {
			out = append(out, t)
		}
	}
	return out
}

func renderTaskPicker(out io.Writer, view []tasks.Task, showAll bool) {
	if len(view) == 0 {
		fmt.Fprintln(out, "(no open tasks; press a to show all statuses, q to quit)")
	}
	for i, t := range view {
		origin := t.OriginSession
		if origin == "" {
			origin = "(none)"
		}
		fmt.Fprintf(out, " [%d] %-12s %-50s · %s\n", i+1, t.Status, t.Text, origin)
	}
	scope := "open"
	if showAll {
		scope = "all"
	}
	fmt.Fprintf(out, "Choose [1-%d] launch · a toggle all (showing %s) · q quit\n> ", len(view), scope)
}
