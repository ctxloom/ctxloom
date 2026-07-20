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
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	taskloomconfig "github.com/ctxloom/ctxloom/internal/taskloom/config"
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
// Ignored (with a warning) in repo-homed mode, where the repo path itself is
// the store's identity — see operations.resolveRepoHomedStore.
var tasksProject string

// tasksHoming is the --homing override: an explicit task-store homing mode
// ("home" or "repo") for this invocation, winning over every config layer
// (home/project .taskloom/config.yaml, TASKLOOM_CONFIG_HOMING, --config-set
// homing=...) — see taskloomconfig.ResolveMode for the full precedence chain
// and its fail-loud behavior when nothing at all sets it.
var tasksHoming string

func init() {
	rootCmd.PersistentFlags().StringVar(&tasksProject, "project", "", "Project id to act on (overrides the session's CTXLOOM_PROJECT_ID pin and cwd resolution)")
	rootCmd.PersistentFlags().StringVar(&tasksHoming, "homing", "",
		`Task-store location for this invocation: "home" keeps it private under ~/.ctxloom/tasks `+
			`(today's default behavior); "repo" checks it into .taskloom/tasks.jsonl so it travels `+
			`with clones. Overrides the `+"`"+taskloomconfig.HomingConfigKey+"`"+` key in .taskloom/config.yaml `+
			`and TASKLOOM_CONFIG_HOMING.`)
	rootCmd.PersistentFlags().StringArray(confload.ConfigSetFlagName, nil,
		"Override a taskloom config value for this invocation: --config-set <dotted.path>=<value> (repeatable)")
}

// taskContext gathers the inputs operations needs to resolve the project task
// log: the project root (CTXLOOM_ROOT override, else git root, else cwd --
// redirected to a linked worktree's primary checkout unless it opted out with
// its own .ctxloom, see workdir.ResolveBoundary), the project-id (--project,
// else the CTXLOOM_PROJECT_ID exported by `ctxloom run`, empty for a bare run
// so operations resolves it live), and the active session harp stamped as
// task provenance.
//
// A stale linked-worktree pointer (the primary checkout is gone) fails the
// whole call UNLESS a project-id is already pinned (--project or
// CTXLOOM_PROJECT_ID): a pin doesn't need WorkDir for identity at all, and
// blocking an otherwise well-identified operation on an unrelated stale
// worktree pointer would be its own silent-trust regression in the other
// direction. The pinned-vs-cwd mismatch note resolveTaskStore would
// otherwise compute is simply skipped in that case (WorkDir comes back
// empty).
func taskContext() (operations.TaskContext, error) {
	projectID := tasksProject
	if projectID == "" {
		projectID = os.Getenv("CTXLOOM_PROJECT_ID")
	}
	workDir, err := workdir.Resolve()
	if err != nil {
		if projectID == "" {
			return operations.TaskContext{}, err
		}
		clidiag.Warn("taskloom", "working directory resolution failed: %v", err)
		workDir = ""
	}
	return operations.TaskContext{
		WorkDir:     workDir,
		ProjectID:   projectID,
		SessionHarp: os.Getenv("CTXLOOM_SESSION_HARP"),
	}, nil
}

// resolveHoming resolves tc's task-store homing mode via
// taskloomconfig.ResolveMode — taskloom's own layered config (home < project
// .taskloom/config.yaml < TASKLOOM_CONFIG_HOMING env < --config-set) then the
// dedicated --homing flag (highest precedence) — and sets it on tc.
//
// When NOTHING at any layer sets it, ResolveMode silently defaults to
// paths.ModeHome (today's pre-homing behavior — see
// taskloomconfig's package doc for why only that direction is safe to
// default without asking); this only ever returns an error for a value that
// IS set but invalid (anything other than "home"/"repo").
//
// Every command that is about to touch ONE project's store calls this right
// after taskContext() (taskContextSingle does both in one call); `taskloom
// list`/task_list's --global aggregation, and the no-project fallback it
// shares, never do — homing mode is meaningless for a read that spans every
// project's store at once.
func resolveHoming(tc operations.TaskContext) (operations.TaskContext, error) {
	mode, err := taskloomconfig.ResolveMode(tc.WorkDir, rootCmd.PersistentFlags(), tasksHoming)
	if err != nil {
		return tc, err
	}
	tc.HomingMode = mode
	return tc, nil
}

// taskContextSingle is taskContext() plus resolveHoming: the combined
// resolution every command that touches exactly one project's store needs.
func taskContextSingle() (operations.TaskContext, error) {
	tc, err := taskContext()
	if err != nil {
		return tc, err
	}
	return resolveHoming(tc)
}

// isInteractiveTerminal returns true if both stdin and stdout are terminals.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// noteTaskProject names the store a mutation landed in, on stderr so stdout
// stays parseable. A pinned project-id wins over cwd, so without this a task
// added while cd'd into another project lands somewhere invisible. In
// repo-homed mode projectID is always empty (the repo path IS the identity —
// see operations.resolveRepoHomedStore), so the "(id)" suffix is only shown
// when there is actually an id to show.
func noteTaskProject(projectID, projectDir string) {
	fmt.Fprintf(os.Stderr, "taskloom: project %s\n", formatProjectLabel(projectDir, projectID))
}

// formatProjectLabel renders a "dir (id)" / "dir" / "id" label for a
// resolved store, omitting the "(id)" suffix when id is empty (repo-homed
// mode mints no project-id — see operations.resolveRepoHomedStore) rather
// than printing a bare, confusing "()" — used by noteTaskProject and every
// place `taskloom list`/`taskloom tags` names the resolved store.
func formatProjectLabel(dir, id string) string {
	switch {
	case dir != "" && id != "":
		return fmt.Sprintf("%s (%s)", dir, id)
	case dir != "":
		return dir
	default:
		return id
	}
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
