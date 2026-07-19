package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/taskloom/workdir"
)

var rootCmd = &cobra.Command{
	Use:   "taskloom",
	Short: "Manage the per-project task store",
	Long: `Read and modify the per-project task store. Tasks are keyed by harp IDs
(e.g. "swift-amber-falcon") and persisted as an append-only log at
~/.ctxloom/tasks/<project-id>.jsonl.

The project is resolved from CTXLOOM_PROJECT_ID (exported by ctxloom run),
--project, or the working directory's identity marker/registry, in that order
of precedence. Agents reach the same store via the MCP tools served by
` + "`taskloom mcp`" + ` (task_list, task_add, task_set_status, task_edit, task_tag).

Tasks carry flat tags: apply them with ` + "`taskloom tag`" + ` (or ` + "`add --tag`" + `), see the
vocabulary in use with ` + "`taskloom tags`" + `, and filter with ` + "`taskloom list --tag-query`" + `.`,
	SilenceUsage: true,
	// Flip clidiag's structured-diagnostics channel on for json/yaml/toml
	// --format, off for text/markdown or an unresolvable value — mirroring
	// cmd/ctxloom, so a machine-readable listing carries machine-readable
	// warnings too. An invalid --format is reported by the command's own
	// emit()/resolveFormat call, not here, so this just falls back to the safe
	// default rather than erroring twice.
	PersistentPreRun: func(cmd *cobra.Command, _ []string) {
		format, ferr := cliemit.Resolve(cmd)
		clidiag.SetStructured(ferr == nil && format.Structured())
	},
}

// tasksProject is the --project override: an explicit project-id to act on,
// winning over both the session's CTXLOOM_PROJECT_ID pin and cwd resolution.
var tasksProject string

func init() {
	rootCmd.PersistentFlags().StringVar(&tasksProject, "project", "", "Project id to act on (overrides the session's CTXLOOM_PROJECT_ID pin and cwd resolution)")
}

// taskContext gathers the inputs operations needs to resolve the project task
// log: the project root (CTXLOOM_ROOT override, else git root, else cwd), the
// project-id (--project, else the CTXLOOM_PROJECT_ID exported by `ctxloom run`,
// empty for a bare run so operations resolves it live), and the active session
// harp stamped as task provenance.
func taskContext() operations.TaskContext {
	projectID := tasksProject
	if projectID == "" {
		projectID = os.Getenv("CTXLOOM_PROJECT_ID")
	}
	return operations.TaskContext{
		WorkDir:     workdir.Resolve(),
		ProjectID:   projectID,
		SessionHarp: os.Getenv("CTXLOOM_SESSION_HARP"),
	}
}

// isInteractiveTerminal returns true if both stdin and stdout are terminals.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// noteTaskProject names the store a mutation landed in, on stderr so stdout
// stays parseable. A pinned project-id wins over cwd, so without this a task
// added while cd'd into another project lands somewhere invisible.
func noteTaskProject(projectID, projectDir string) {
	if projectDir != "" {
		fmt.Fprintf(os.Stderr, "taskloom: project %s (%s)\n", projectDir, projectID)
		return
	}
	fmt.Fprintf(os.Stderr, "taskloom: project %s\n", projectID)
}

// warnTask surfaces a project-resolution notice (move/fork) returned by an
// operations task call.
func warnTask(warning string) {
	if warning != "" {
		clidiag.Warn("taskloom", "%s", warning)
	}
}

// summaryWidth caps the task text shown in the default human `list` so entries
// stay scannable instead of running together into a wall; `--full` prints the
// whole text.
const summaryWidth = 80

// summarize collapses s to its first line, capped to width runes with a
// trailing ellipsis when truncated. Multi-line or long task text becomes a
// single scannable line; the machine-readable views (--json / --format) keep
// the full text.
func summarize(s string, width int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, " ")
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return strings.TrimRight(string(r[:width]), " ") + "…"
}

// renderTaskTable prints the human `list` view: one entry per task, blank-line
// separated so adjacent tasks are easy to tell apart, each a scannable one-line
// summary. The harp id is never truncated (it's a copy-paste identifier); the
// text is summarized — a task's full text is `taskloom show <harp>` (or the
// machine-readable --format json).
func renderTaskTable(out io.Writer, list []tasks.Task) error {
	w := iox.NewErrWriter(out)
	if len(list) == 0 {
		w.Println("(no tasks)")
		return w.Err()
	}
	// Pad the harp-id column to the widest id in this list so the columns line
	// up regardless of id length.
	idWidth := 0
	for _, t := range list {
		if len(t.HarpID) > idWidth {
			idWidth = len(t.HarpID)
		}
	}
	for i, t := range list {
		if i > 0 {
			// Blank line between entries so a long list reads as distinct
			// tasks rather than one undifferentiated block.
			w.Println("")
		}
		check := " "
		if t.Checked {
			check = "x"
		}
		text := summarize(t.Text, summaryWidth)
		if t.Trigger != "" {
			text = fmt.Sprintf("%s  (trigger: %s)", text, t.Trigger)
		}
		if len(t.Tags) > 0 {
			text = fmt.Sprintf("%s  [%s]", text, strings.Join(t.Tags, ", "))
		}
		w.Printf("[%s] %-*s  %-11s  %s\n", check, idWidth, t.HarpID, t.Status, text)
	}
	return w.Err()
}
