package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	taskloomconfig "github.com/ctxloom/ctxloom/internal/taskloom/config"
	"github.com/ctxloom/ctxloom/internal/taskloom/workdir"
)

// progName is the one name this binary answers to: the cobra Use line, the PATH
// lookup `manage` performs, the MCP server name, the loadout name, every
// diagnostic prefix, and the `.name` field of `taskloom version --format json`
// that ctxloom's boot probe parses to identify the companion.
const progName = "taskloom"

var rootCmd = &cobra.Command{
	Use:   progName,
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
	// cobra's own error print knows nothing about --format, so it would emit a
	// human "Error: <msg>" line into a stream the caller asked to be json/yaml/
	// toml. main's tail (reportExecuteError) reports the failure through the
	// shared format filter instead; without this the two would BOTH print.
	SilenceErrors: true,
	// Flip clidiag's structured-diagnostics channel on for json/yaml/toml
	// --format, off for text/markdown or an unresolvable value — mirroring
	// cmd/ctxloom, so a machine-readable listing carries machine-readable
	// warnings too. An invalid --format is reported by the command's own
	// emit()/resolveFormat call, not here, so this just falls back to the safe
	// default rather than erroring twice.
	PersistentPreRun: rootPersistentPreRun,
}

func rootPersistentPreRun(cmd *cobra.Command, _ []string) {
	format, ferr := cliemit.Resolve(cmd)
	clidiag.SetStructured(ferr == nil && format.Structured())
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
// A failure to resolve the work root — notably a stale linked-worktree
// pointer, where the primary checkout is gone — fails the whole call, whether
// or not a project-id is pinned. A pin identifies WHICH project, but WorkDir
// is not only an identity input: it is the sole anchor for the project config
// layer (taskloomconfig.Load reads <WorkDir>/.taskloom/config.yaml, and
// projectConfigPath drops that layer entirely for an empty dir). Proceeding
// with an empty WorkDir therefore silently discards whatever that layer
// declared — a `homing: repo` project resolves to ModeHome and writes to
// ~/.ctxloom/tasks/<pinned-id>.jsonl instead of its checked-in log, and a
// project-declared tag_schema degrades to the default, so tag values it would
// have rejected are accepted. Both succeed, report success, and leave the
// bytes somewhere the project never reads.
//
// Refusing is also what workdir.ResolveBoundary's own contract already
// promises ("never falls back to minting a task store nobody will find
// again"); the remedy is in the error it returns.
func taskContext() (operations.TaskContext, error) {
	projectID := tasksProject
	if projectID == "" {
		projectID = os.Getenv("CTXLOOM_PROJECT_ID")
	}
	workDir, boundary, err := workdir.ResolveBoundary()
	if err != nil {
		return operations.TaskContext{}, err
	}
	return operations.TaskContext{
		WorkDir:           workDir,
		WorkDirIsBoundary: boundary,
		ProjectID:         projectID,
		SessionHarp:       os.Getenv("CTXLOOM_SESSION_HARP"),
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

// resolveTagSchema resolves tc's tag-schema (taskloom's own layered config —
// home < project .taskloom/config.yaml < TASKLOOM_CONFIG_* env <
// --config-set tag_schema=..., via taskloomconfig.Load — falling back to
// taskloomconfig.DefaultTagSchema when unset at every layer, see
// Config.ResolvedTagSchema's doc) and sets it on tc. A malformed declaration
// is a returned error (fail loud: never run with a silently empty or
// partial schema) naming the offending declaration.
func resolveTagSchema(tc operations.TaskContext) (operations.TaskContext, error) {
	cfg, err := taskloomconfig.Load(tc.WorkDir, rootCmd.PersistentFlags())
	if err != nil {
		return tc, err
	}
	schema, err := cfg.ParsedTagSchema()
	if err != nil {
		return tc, err
	}
	tc.TagSchema = schema
	return tc, nil
}

// taskContextSingle is taskContext() plus the homing mode and the tag-schema:
// the combined resolution every command that touches exactly one project's
// store needs.
//
// It layers the config ONCE and resolves both settings from that one value,
// rather than calling resolveHoming and resolveTagSchema in turn — those two
// exist for the callers that need exactly one of the pair, and each Loads on
// its own, so using them here read and merged home+project config.yaml twice
// on the startup path of every single-project command. Resolution order is
// unchanged: an invalid homing value is still reported before a malformed
// tag-schema declaration.
func taskContextSingle() (operations.TaskContext, error) {
	tc, err := taskContext()
	if err != nil {
		return tc, err
	}
	cfg, err := taskloomconfig.Load(tc.WorkDir, rootCmd.PersistentFlags())
	if err != nil {
		return tc, err
	}
	mode, err := cfg.ResolveMode(tasksHoming)
	if err != nil {
		return tc, err
	}
	schema, err := cfg.ParsedTagSchema()
	if err != nil {
		return tc, err
	}
	tc.HomingMode = mode
	tc.TagSchema = schema
	return tc, nil
}

// isInteractiveTerminal returns true if both stdin and stdout are terminals.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// noteTaskProject names the store a mutation landed in, on stderr so stdout
// stays parseable. Its parameters are in formatProjectLabel's order — the two
// take the same pair of same-typed strings and nothing in the type system
// separates them, so a second order is a transposition waiting to happen. A pinned project-id wins over cwd, so without this a task
// added while cd'd into another project lands somewhere invisible. In
// repo-homed mode projectID is always empty (the repo path IS the identity —
// see operations.resolveRepoHomedStore), so the "(id)" suffix is only shown
// when there is actually an id to show.
func noteTaskProject(projectDir, projectID string) {
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
		clidiag.Warn(progName, "%s", warning)
	}
}

// renderTaskTable prints the human `list` view: one entry per task, blank-line
// separated so adjacent tasks are easy to tell apart, each a scannable one-line
// summary. The harp id is never truncated (it's a copy-paste identifier); the
// text is summarized — a task's full text is `taskloom show <harp>` (or the
// machine-readable --format json). cfg is applied to each task's tags via
// visibleTags before printing — see hideConfigFor.
func renderTaskTable(out io.Writer, list []tasks.Task, cfg tagma.HideConfig) error {
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
		text := tasks.Headline(t.Text)
		if t.Trigger != "" {
			text = fmt.Sprintf("%s  (trigger: %s)", text, t.Trigger)
		}
		if visible := visibleTags(t.Tags, cfg); len(visible) > 0 {
			text = fmt.Sprintf("%s  [%s]", text, strings.Join(visible, ", "))
		}
		w.Printf("[%s] %-*s  %-11s  %s\n", check, idWidth, t.HarpID, t.Status, text)
	}
	return w.Err()
}
