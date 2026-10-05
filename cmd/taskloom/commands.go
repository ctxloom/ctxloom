package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/priority"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// sortPriority is the only recognized `--sort` / task_list `sort` value
// today: derived, rank-normalized priority (internal/shared/tasks/priority),
// descending. Anything else (including the default "") leaves a listing in
// its existing order — this feature is purely additive, never a change to
// what an unmodified `list`/task_list call returns.
const sortPriority = "priority"

var (
	tasksListStatuses []string
	tasksListTerm     string
	tasksListTagQuery string
	tasksListAll      bool
	tasksListGlobal   bool
	tasksListSort     string
	tasksListCompact  bool
	tasksListLimit    int
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

--global (explicit or fallback) only ever aggregates PRIVATELY-homed projects
under ~/.ctxloom/tasks; a repo-homed project (homing: repo, its log checked
into <repo>/.taskloom/tasks.jsonl) is registered nowhere global and is never
included, even if it's the very project you're standing in. Every --global
listing says so on stderr.

By default only active tasks are shown: completed (Done/Archived) and
Deferred tasks are hidden. Pass --all to include them, or name a status
explicitly with --status (an explicit status filter is honored verbatim;
see "taskloom statuses" for the taxonomy). --status also accepts the @-classes
--status @open (every non-terminal status) and --status @terminal (every
completed one), expanded from that same taxonomy at RUNTIME: a status added to
it is picked up by the right class with nothing here to keep in sync. Classes
mix freely with literal statuses in one filter, since this is sugar over the
already-repeatable --status. When a --term or --tag-query
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

  # every task that is not finished — To Do, In Progress, Deferred, and
  # whatever else the taxonomy calls non-terminal
  taskloom list --status @open

  # a class mixed with a literal
  taskloom list --status @terminal --status "In Progress"

  # every project's tasks, not just the current one
  taskloom list --global`,
	RunE: runList,
}

func runList(cmd *cobra.Command, args []string) error {
	format, err := cliemit.Resolve(cmd)
	if err != nil {
		return err
	}
	tc, err := taskContext()
	if err != nil {
		return err
	}
	// Resolved unconditionally (not just when --sort priority is passed):
	// it's a cheap config read, and priority computation needs it — see
	// runListCmd's --sort priority branch.
	tc, err = resolveTagSchema(tc)
	if err != nil {
		return err
	}
	return runListCmd(cmd.OutOrStdout(), os.Stderr, tc, listOptions{
		Statuses: tasksListStatuses,
		Term:     tasksListTerm,
		TagQuery: tasksListTagQuery,
		All:      tasksListAll,
		Global:   tasksListGlobal,
		Sort:     tasksListSort,
		Compact:  tasksListCompact,
		Limit:    tasksListLimit,
		Format:   format,
	})
}

// listOptions bundles the resolved inputs for `taskloom list`. It replaces a
// long positional parameter list — several same-typed bools in a row are easy
// to transpose at a call site, and a named field says what each one means.
type listOptions struct {
	Statuses []string
	Term     string
	TagQuery string
	All      bool   // include the default-hidden tasks: Done/Archived and Deferred
	Global   bool   // aggregate across every project, not just the resolved one
	Sort     string // "" (default: unsorted) or sortPriority

	// Compact, when true, renders each row as its CompactTask projection
	// (harp id, status, checked, tags, headline — see internal/shared/tasks.
	// Task.Compact) instead of the full task body, for a machine format
	// (json/yaml/toml/markdown). Ignored for the default text view, which is
	// already a one-line-per-task summary (renderTaskTable's own Headline
	// call).
	Compact bool

	// Limit caps the number of rows returned (0 = no cap, today's unchanged
	// default). Applied at the query layer AFTER every filter and the
	// default active-only pass; rows it cuts are reported on stderr
	// (noteOmittedByLimit) — status/summary counts are never affected, since
	// a summary is its own unfiltered read (scopedSummary).
	Limit int

	Format clifmt.Format
}

// runListCmd is listCmd's RunE body, factored out so it can be driven in
// tests without cobra machinery: out/errw are separate so a test can assert
// on each independently, matching the command's real stdout-stays-parseable
// / stderr-carries-diagnostics split. text renders the human table(s); any
// other format hands the raw rows to clifmt so the same data serializes to
// json/yaml/toml/markdown without a per-format branch here.
func runListCmd(out, errw io.Writer, tc operations.TaskContext, opts listOptions) error {
	// Named for the flag the user typed. Each surface spells its own option
	// (--sort here, the sort field over MCP); listTasksScoped refuses an
	// unknown value too, so neither can fall through to a silent unsorted
	// listing.
	if opts.Sort != "" && opts.Sort != sortPriority {
		return fmt.Errorf("taskloom: unknown --sort value %q (must be %q)", opts.Sort, sortPriority)
	}
	r, err := listTasksScoped(tc, opts)
	if err != nil {
		return err
	}
	if r.Notice != "" {
		clidiag.Fwarn(errw, progName, "%s", r.Notice)
	}
	// One Fwarn per line: clidiag prefixes what it is handed, so passing a
	// multi-line warning as one message leaves every line after the first
	// unprefixed on stderr — indistinguishable from ordinary output, which
	// is the contract the diagnostic channel exists to keep.
	for _, line := range splitLines(r.PriorityWarning) {
		clidiag.Fwarn(errw, progName, "%s", line)
	}
	noteHidden(errw, r.HiddenCompleted, r.HiddenDeferred, r.Filtered)
	noteOmittedByLimit(errw, r.OmittedByLimit)

	if r.Global {
		return renderGlobalListing(out, r, opts)
	}
	return renderProjectListing(out, r, opts)
}

// renderGlobalListing writes an aggregated listing: project-attributed rows in
// a machine format, or one table section per project in the text view.
func renderGlobalListing(out io.Writer, r *scopedListResult, opts listOptions) error {
	if opts.Format != clifmt.FormatText {
		if opts.Compact {
			return clifmt.Render(out, compactRows(r.Rows), opts.Format)
		}
		return clifmt.Render(out, r.Rows, opts.Format)
	}
	w := errwriter.New(out)
	w.Printf("Projects: %d (--global)\n\n", r.ProjectCount)
	if err := w.Err(); err != nil {
		return err
	}
	return renderGlobalTaskTable(out, r.Rows, hideConfigFor(r.TC))
}

// renderProjectListing writes a single-project listing. Its machine formats
// emit unattributed tasks — the project is named once, not per row.
func renderProjectListing(out io.Writer, r *scopedListResult, opts listOptions) error {
	if opts.Format != clifmt.FormatText {
		if opts.Compact {
			return clifmt.Render(out, compactTasksOf(r.Tasks), opts.Format)
		}
		return clifmt.Render(out, r.Tasks, opts.Format)
	}
	// Name the resolved store: in multi-root workspaces (several .ctxloom
	// trees under one repo), which project a listing came from is the
	// first thing a confused reader needs to know.
	w := errwriter.New(out)
	w.Printf("Project: %s\n\n", formatProjectLabel(r.ProjectDir, r.ProjectID))
	if err := w.Err(); err != nil {
		return err
	}
	return renderTaskTable(out, r.Tasks, hideConfigFor(r.TC))
}

// compactTasksOf projects a single-project listing's tasks to their
// CompactTask presentation form (see internal/shared/tasks.Task.Compact),
// for `taskloom list --compact --format json` (etc.) — mirrors compactRows
// for the --global path, which additionally carries each row's project id.
func compactTasksOf(list []tasks.Task) []tasks.CompactTask {
	out := make([]tasks.CompactTask, len(list))
	for i, t := range list {
		out[i] = t.Compact()
	}
	return out
}

// attachPriority sets each task's DerivedPriority (in place) from results,
// looked up by harp ID. A harp missing from results (e.g. a task added in
// the narrow race between the normalization snapshot and this page) is left
// at 0 rather than failing the whole listing over it.
func attachPriority(list []tasks.Task, results map[string]priority.Result) {
	for i := range list {
		p := 0.0
		if r, ok := results[list[i].HarpID]; ok {
			p = r.Priority
		}
		list[i].DerivedPriority = &p
	}
}

// sortTasksByPriorityDesc stable-sorts list by DerivedPriority descending
// (highest priority first); ties keep their existing relative order (add
// order, or whatever a prior filter/sort left them in).
func sortTasksByPriorityDesc(list []tasks.Task) {
	sort.SliceStable(list, func(i, j int) bool {
		return priorityOf(list[i]) > priorityOf(list[j])
	})
}

func priorityOf(t tasks.Task) float64 {
	if t.DerivedPriority == nil {
		return 0
	}
	return *t.DerivedPriority
}

// thinCoverageFraction is the share of the active population a formula term
// must reach before this warning stops naming it. Below it, the MAJORITY of
// tasks are ranked by that term's absence rather than by anything they
// carry, which makes the term a describer of exceptions rather than of the
// log — the exact shape of the miss this warning exists to catch, where a
// hand-applied axis reached a sixth of the log and the ranking read as
// healthy anyway.
const thinCoverageFraction = 0.5

// priorityDiagnosticWarning renders a priority.Diagnostics into a
// plain-English warning when `--sort priority`/task_list's sort="priority"
// just produced a ranking that EXISTS but is MEANINGLESS (see that type's
// doc) — or "" when the ranking is fine. NoPriorityFn is checked first and
// reported ALONE, short-circuiting everything below: with no formula there
// is no term whose coverage could be discussed, and the fix (declare a
// priority_fn) is a different fix from a genuinely-tied population's (apply
// the tags the formula reads). The tied wording quotes ScoredTasks against
// its NonTerminalTasks denominator: the numerator alone says nothing, since
// the same "only 3" is a healthy ranking of 3 active tasks and a broken one
// of 300.
//
// A ranking that is NOT degenerate still gets one line per thinly-covered
// formula term (see thinCoverageWarnings) — a ranking decided by two of six
// terms is not wrong, but the four that decided nothing must be visible or
// the next reader trusts an ordering the data cannot support.
func priorityDiagnosticWarning(d priority.Diagnostics) string {
	if d.NoPriorityFn {
		return "--sort priority is meaningless here: this project's tag_schema declares no priority_fn, so every task's raw score is 0 and the ranking reflects nothing"
	}
	var lines []string
	if d.AllTied {
		lines = append(lines, fmt.Sprintf("--sort priority is meaningless here: every active task ties at the same raw priority score (only %d of %d active tasks carry a tag any priority_fn/decay_fn formula actually reads) — the ranking reflects nothing", d.ScoredTasks, d.NonTerminalTasks))
	}
	lines = append(lines, thinCoverageWarnings(d)...)
	return strings.Join(lines, "\n")
}

// splitLines splits a possibly-multi-line diagnostic into its lines,
// yielding nothing at all for an empty one (strings.Split would hand back a
// single empty line, which renders as a bare "warning:" with no message).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// thinCoverageWarnings renders one line per formula-referenced target that
// reaches fewer than thinCoverageFraction of the active population, worst
// first (priority.Diagnostics.TargetCoverage is already in that order). A
// term carried by NO task gets its own wording: it is not thinly grounded,
// it is inert — it evaluates to the same constant everywhere and can never
// move the ranking, which is a schema defect rather than a tagging backlog.
// The thin case instead names the UNCOVERED count, because that is the
// number a reader has to act on: those tasks are all ranked as if the tag
// were absent, which for a floor-valued term means ranked at the floor.
//
// An empty population yields nothing: with no active tasks every term is
// trivially uncovered and there is no ranking to mislead anyone.
func thinCoverageWarnings(d priority.Diagnostics) []string {
	if d.NonTerminalTasks == 0 {
		return nil
	}
	var out []string
	for _, c := range d.TargetCoverage {
		if float64(c.Tasks) >= thinCoverageFraction*float64(d.NonTerminalTasks) {
			continue
		}
		if c.Tasks == 0 {
			out = append(out, fmt.Sprintf("priority_fn/decay_fn reads %s, which none of the %d active tasks carries — that term is inert and moves no task's rank", c.Target, d.NonTerminalTasks))
			continue
		}
		out = append(out, fmt.Sprintf("%d of %d active tasks carry no %s — they rank as if it were absent, on a formula that reads it", d.NonTerminalTasks-c.Tasks, d.NonTerminalTasks, c.Target))
	}
	return out
}

// wrapTagQueryError names the postfix grammar as a malformed --tag-query's
// remedy (report.Error), shared by both the single-project and --global list
// paths; RenderError prints it as the fix line, and the MCP tools' remedied
// wrapper carries it into the tool error. tagma reports a malformed query as
// a plain error (no dedicated type to type-assert), so this checks for
// tasks.ErrTagQuery via errors.Is instead — the sentinel filterTasks wraps
// every tagma query error with — to tell "the --tag-query itself is bad"
// apart from an unrelated error (store I/O, project resolution) that
// shouldn't get the remedy. The remedy names "operand" explicitly (not just
// tagma's own "stack underflow" wording) so a malformed query keeps failing
// loud with the problem named in the same vocabulary it always has,
// independent of the query engine underneath.
func wrapTagQueryError(err error) error {
	if err == nil || !errors.Is(err, tasks.ErrTagQuery) {
		return err
	}
	return report.Error{Fix: tagQueryRemedy, Err: err}
}

const tagQueryRemedy = "queries are postfix: tags first, operator after — e.g. urgent/release/and, urgent/not; an and/or/not operator needs enough operands already on the query's stack, or it fails (see 'taskloom list --help' for more)"

// renderGlobalTaskTable prints a --global (or no-project-fallback) listing as
// one table section per project, reusing renderTaskTable per section so the
// per-row formatting matches the single-project view exactly. cfg is passed
// straight through to each section's renderTaskTable.
func renderGlobalTaskTable(out io.Writer, rows []taskRow, cfg tagma.HideConfig) error {
	w := errwriter.New(out)
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
		if err := renderTaskTable(out, plain, cfg); err != nil {
			return err
		}
		w.Println("")
		start = i
	}
	return w.Err()
}

// noteHidden tells the user (on stderr, so stdout stays parseable) when the
// default active-only view suppressed tasks their filter matched. Only a
// searching intent (--term / --tag-query) earns the note: a bare `list` is a
// "show my active work" view where hiding finished tasks is the whole point,
// but a query that matches 49 tasks and prints 11 with no trace is a silent
// truncation of an answer. Takes the counts directly rather than an
// operations.TaskListResult — the --global aggregation path sums counts
// across several projects' stores rather than getting them from one
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

// noteOmittedByLimit tells the user (on stderr, so stdout/the machine format
// stays parseable) how many rows a positive --limit cut off the end of an
// otherwise-larger result, so a capped listing never reads as a complete one.
// A no-op when limit wasn't set or the result already fit within it.
// Status/summary counts are never affected by the cap — see
// internal/shared/tasks/operations.TaskListResult.OmittedByLimit's doc.
func noteOmittedByLimit(w io.Writer, omitted int) {
	if omitted <= 0 {
		return
	}
	fmt.Fprintf(w, "taskloom: %d more task(s) omitted by --limit — raise or drop --limit to see them (status/summary counts are unaffected)\n", omitted)
}

var (
	tasksAddStatus  string
	tasksAddTrigger string
	tasksAddTags    []string
)

var addCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Add a new task",
	Long: `Add a new task.

Make the first line the subject: what the task IS, in ~80 characters or
fewer. The default list view shows only that first line, truncated at 80
runes, so provenance (dates, session names, commit SHAs, "found while doing
X") belongs on a later line, not the first — otherwise it eats the summary
budget before the subject appears. Full text is always available via
"taskloom show <harp-id>".

Record only what is STILL TO DO. A task is a work item, not a history of
one. When part of it lands, EDIT the task down to what remains — do not
append "ITEM 1 DONE, items 2-13 remain". A task carrying its own finished
half reads as open work forever: every reader has to re-derive what is
actually left, and a triage pass cannot tell it from work never started.
What was completed belongs in the commit message, not here.

Locate the work by SIGNATURE, not by position. Name the function, method,
type, or exact string to search for — "cloneMCPServer in internal/core/config",
"the Changed --json branch in cliemit.Resolve" — never "accessors.go:95".
Line numbers drift on every edit above them and are usually wrong by the
time anyone reads the task; a symbol name still finds it.

Text that begins with "-" (a subject naming the flag it is about) is parsed
as a flag however it is quoted; put -- before it to end flag parsing.`,
	Example: `  taskloom add "ship the release notes" --tag release --tag docs
  taskloom add "investigate flaky TestFoo" --status "In Progress"
  taskloom add "revisit caching" --status Deferred --trigger "the v2 API ships"
  taskloom add "dedupe the retry loop in the sync client
(found 2026-07-19, session icy-weary-chimp, while reviewing config layering)"
  taskloom add --tag cli -- "--json drops the error envelope"`,
	Args: textArgs(1),
	RunE: runAdd,
}

// errTextReadAsFlag is the remedy add and edit attach to a flag-parse or
// argument-count failure. pflag sees argv, not the shell's quoting, so task
// text beginning with "-" is parsed as a flag however it was quoted; that
// surfaces as an unknown flag, a flag missing its value, or (when the text is
// exactly a real flag) too few arguments, and none of those messages says the
// text was the cause.
var errTextReadAsFlag = errors.New(`if the task text begins with "-", it was parsed as a flag: put -- before the text to end flag parsing`)

// textArgs is cobra.MinimumNArgs(n) for a command whose trailing positionals
// are task text, naming errTextReadAsFlag when too few arrive.
func textArgs(n int) cobra.PositionalArgs {
	minArgs := cobra.MinimumNArgs(n)
	return func(cmd *cobra.Command, args []string) error {
		if err := minArgs(cmd, args); err != nil {
			return fmt.Errorf("%w; %w", err, errTextReadAsFlag)
		}
		return nil
	}
}

// textFlagError is the FlagErrorFunc of a command that takes task text,
// naming errTextReadAsFlag alongside pflag's own error.
func textFlagError(_ *cobra.Command, err error) error {
	return fmt.Errorf("%w; %w", err, errTextReadAsFlag)
}

func runAdd(cmd *cobra.Command, args []string) error {
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
	noteTaskProject(res.ProjectDir, res.ProjectID)
	task := res.Task
	return cliemit.Emit(cmd, task, func() error {
		w := errwriter.New(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	})
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
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	tc, err := taskContextSingle()
	if err != nil {
		return err
	}
	res, err := operations.SetTaskStatus(tc, args[0], args[1], tasksStatusTrigger)
	if err != nil {
		return err
	}
	warnTask(res.Warning)
	noteTaskProject(res.ProjectDir, res.ProjectID)
	task := res.Task
	return cliemit.Emit(cmd, task, func() error {
		w := errwriter.New(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	})
}

var editCmd = &cobra.Command{
	Use:   "edit <harp-id> <text>",
	Short: "Replace a task's text in place (full new text)",
	Long: `Replace a task's text, keyed by its harp ID.

The entire text is replaced with what you pass (not patched); the task's
status and any Deferred trigger are left unchanged.

Text that begins with "-" is parsed as a flag however it is quoted; put --
before it to end flag parsing.`,
	Example: `  taskloom edit swift-amber-falcon "the new full text"
  taskloom edit swift-amber-falcon -- "--json drops the error envelope"`,
	Args: textArgs(2),
	RunE: runEdit,
}

func runEdit(cmd *cobra.Command, args []string) error {
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
	noteTaskProject(res.ProjectDir, res.ProjectID)
	task := res.Task
	return cliemit.Emit(cmd, task, func() error {
		w := errwriter.New(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	})
}

var (
	tasksTagAdd    []string
	tasksTagRemove []string
)

var tagCmd = &cobra.Command{
	Use:   "tag <harp-id>",
	Short: "Add and/or remove tags on a task",
	Long: `Add and/or remove tags on a task, keyed by its harp ID. A tag is
(namespace:)key(=value): "urgent" and "triage:kind=defect" are both tags.

--add and --remove are each repeatable; at least one is required. --add is
applied before --remove, so a tag named in both ends up removed. See the
tags already in use with "taskloom tags"; filter tasks by tag with
"taskloom list --tag-query".`,
	Example: `  taskloom tag swift-amber-falcon --add urgent --add release
  taskloom tag swift-amber-falcon --remove urgent --add blocked`,
	Args: cobra.ExactArgs(1),
	RunE: runTag,
}

func runTag(cmd *cobra.Command, args []string) error {
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
	noteTaskProject(res.ProjectDir, res.ProjectID)
	task := res.Task
	return cliemit.Emit(cmd, task, func() error {
		return renderTagResult(cmd.OutOrStdout(), task, hideConfigFor(tc))
	})
}

// renderTagResult prints `taskloom tag`'s human line for t: harp id, status
// and its tags, filtered through cfg via visibleTags like every other
// listing site — see hideConfigFor.
func renderTagResult(out io.Writer, t tasks.Task, cfg tagma.HideConfig) error {
	w := errwriter.New(out)
	w.Printf("%s\t%s\t%s\n", t.HarpID, t.Status, strings.Join(visibleTags(t.Tags, cfg), ","))
	return w.Err()
}

var (
	tasksTagsStatuses []string
	tasksTagsTerm     string
	tasksTagsTagQuery string
	tasksTagsGlobal   bool

	tasksSummaryGlobal bool
)

var tagsCmd = &cobra.Command{
	Use:   "tags",
	Short: "List the tags in use, with per-tag task counts",
	Long: `List every tag currently in use across the project's tasks, with counts.

"active" is the number of tasks carrying the tag that are visible in the
default list view (not completed, not Deferred); "total" is every task
carrying it, however parked or finished. A tag with 0 active but many total
marks a finished workstream; a tag with a single total next to a popular
near-twin is probably a typo.

The SAME filter and scope axes ` + "`taskloom list`" + ` takes narrow the population the
counts are computed over: --status (including the @open/@terminal classes),
--term, --tag-query, and --global/--project. With no flags the counts cover
the whole project, exactly as they always have. This is what answers "how
does this filtered set break down by tag" in one call — the audit found that
question being scraped instead, by piping a tag-query listing through grep,
sort and uniq -c.

--term filters TASKS by a substring of their text, precisely as
` + "`taskloom list --term`" + ` does: it narrows which tasks are counted. It does NOT
filter tag NAMES by substring — every tag carried by a matching task is
listed, whatever it is called. Filtering the vocabulary itself stays a job
for grep or jq over this command's --format json output.

There is no --all: the two count columns already carry that distinction.
"active" IS the default-list-view population and "total" IS the
everything-included one, so the tasks hidden from a listing are always
counted here, and a flag to reveal them would have nothing left to reveal.

Apply tags with "taskloom tag" or "taskloom add --tag"; filter tasks by them
with "taskloom list --tag-query".`,
	Example: `  taskloom tags
  taskloom tags --json

  # how the release-tagged work breaks down by every other tag
  taskloom tags --tag-query release

  # the tag distribution of everything still open
  taskloom tags --status @open

  # tags in use across every privately-homed project
  taskloom tags --global`,
	Args: cobra.NoArgs,
	RunE: runTags,
}

func runTags(cmd *cobra.Command, args []string) error {
	format, err := cliemit.Resolve(cmd)
	if err != nil {
		return err
	}
	tc, err := taskContext()
	if err != nil {
		return err
	}
	// Mirrors runList: the tag-schema drives both the tag-query index's type
	// comparison and the display-hide config this command applies.
	tc, err = resolveTagSchema(tc)
	if err != nil {
		return err
	}
	return runTagsCmd(cmd.OutOrStdout(), os.Stderr, tc, listOptions{
		Statuses: tasksTagsStatuses,
		Term:     tasksTagsTerm,
		TagQuery: tasksTagsTagQuery,
		Global:   tasksTagsGlobal,
		Format:   format,
	})
}

// runTagsCmd is tagsCmd's RunE body, factored out so it can be driven in
// tests without cobra machinery — out/errw are separate for the same reason
// runListCmd's are: stdout stays parseable, stderr carries the diagnostics.
//
// It counts over the task set listTasksScoped selects, which is what gives
// `tags` list's filter and scope axes without a second copy of any of that
// machinery. All is forced on: a tag's `total` is by definition the count
// over every matching task, completed and Deferred included, so letting the
// default active-only view reach this population would silently redefine
// `total` as `active`.
func runTagsCmd(out, errw io.Writer, tc operations.TaskContext, opts listOptions) error {
	opts.All = true
	r, err := listTasksScoped(tc, opts)
	if err != nil {
		return err
	}
	if r.Notice != "" {
		clidiag.Fwarn(errw, progName, "%s", r.Notice)
	}
	counts := operations.TagCountsOf(tasksOfRows(r.Rows))
	// The vocabulary enumeration goes through the same display-hide filter
	// as list/show: a tag hidden by tag_schema's tagma.hide:* declarations
	// shouldn't surface here either — see hideConfigFor.
	visible := visibleTagCounts(counts, hideConfigFor(r.TC))
	if opts.Format != clifmt.FormatText {
		return clifmt.Render(out, visible, opts.Format)
	}
	w := errwriter.New(out)
	if r.Global {
		w.Printf("Projects: %d (--global)\n\n", r.ProjectCount)
	} else {
		w.Printf("Project: %s\n\n", formatProjectLabel(r.ProjectDir, r.ProjectID))
	}
	if err := w.Err(); err != nil {
		return err
	}
	return renderTagCounts(out, visible)
}

// tasksOfRows strips the per-row project attribution a scoped listing carries,
// leaving the plain tasks the tag counter works over. Both scopes populate
// Rows (the single-project path attributes every row to the one project), so
// counting from Rows means the two scopes share one code path.
func tasksOfRows(rows []taskRow) []tasks.Task {
	out := make([]tasks.Task, len(rows))
	for i, r := range rows {
		out[i] = r.Task
	}
	return out
}

// renderTagCounts prints the human `tags` table: one padded row per tag with
// its two labeled counts, or a hint when the population carries no tags at
// all — an empty table would otherwise read as a broken command.
func renderTagCounts(out io.Writer, visible []operations.TagCount) error {
	w := errwriter.New(out)
	if len(visible) == 0 {
		w.Println("(no tags in use — apply one with `taskloom tag <harp-id> --add <tag>`)")
		return w.Err()
	}
	// Pad the tag column so the counts align; the counts are labeled so
	// the output is self-describing without a header row.
	tagWidth := 0
	for _, t := range visible {
		if len(t.Tag) > tagWidth {
			tagWidth = len(t.Tag)
		}
	}
	for _, t := range visible {
		w.Printf("%-*s  %3d active  %3d total\n", tagWidth, t.Tag, t.Active, t.Total)
	}
	return w.Err()
}

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show per-status counts and active in-progress tasks",
	Long: `Show per-status counts and the tasks currently in progress.

Counts cover every task, completed and Deferred included. With --global they
are summed across every privately-homed project, and each in-progress task is
named with its project, since a harp id is unique only within one project.`,
	Example: `  # this project's counts
  taskloom summary

  # summed across every privately-homed project
  taskloom summary --global`,
	Args: cobra.NoArgs,
	RunE: runSummary,
}

func runSummary(cmd *cobra.Command, args []string) error {
	format, err := cliemit.Resolve(cmd)
	if err != nil {
		return err
	}
	tc, err := taskContext()
	if err != nil {
		return err
	}
	return runSummaryCmd(cmd.OutOrStdout(), os.Stderr, tc, listOptions{Global: tasksSummaryGlobal, Format: format})
}

// runSummaryCmd is summaryCmd's RunE body, factored out like runTagsCmd so it
// can be driven in tests without cobra machinery. Only Global and Format of
// opts apply: a summary is never filtered.
func runSummaryCmd(out, errw io.Writer, tc operations.TaskContext, opts listOptions) error {
	r, sum, err := scopedSummary(tc, opts.Global)
	if err != nil {
		return err
	}
	if r.Notice != "" {
		clidiag.Fwarn(errw, progName, "%s", r.Notice)
	}
	if opts.Format != clifmt.FormatText {
		return clifmt.Render(out, sum, opts.Format)
	}
	w := errwriter.New(out)
	if r.Global {
		w.Printf("Projects: %d (--global)\n\n", r.ProjectCount)
	}
	// Stable order so output is diffable.
	keys := make([]string, 0, len(sum.Counts))
	for k := range sum.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w.Printf("%s\t%d\n", k, sum.Counts[k])
	}
	if len(sum.InProgress) > 0 {
		names := make([]string, len(sum.InProgress))
		for i, ip := range sum.InProgress {
			names[i] = ip.HarpID
			if r.Global {
				names[i] = fmt.Sprintf("%s (%s)", ip.HarpID, ip.Project)
			}
		}
		w.Printf("\nIn-progress: %s\n", strings.Join(names, ", "))
	}
	return w.Err()
}

// scopedSummary summarizes every task in the scope global selects (with the
// same no-project fallback a listing takes): All on and no filters, so its
// counts never depend on what a listing's filters or limit showed.
func scopedSummary(tc operations.TaskContext, global bool) (*scopedListResult, tasks.Summary, error) {
	r, err := listTasksScoped(tc, listOptions{All: true, Global: global})
	if err != nil {
		return nil, tasks.Summary{}, err
	}
	return r, summarizeRows(r.Rows), nil
}

// summarizeRows counts rows per status and names each in-progress row with its
// project label. Both collections start empty rather than nil so an all-quiet
// summary still marshals as {} and [], and a caller iterates it without first
// testing for null.
func summarizeRows(rows []taskRow) tasks.Summary {
	out := tasks.Summary{Counts: map[string]int{}, InProgress: []tasks.InProgressTask{}}
	for _, r := range rows {
		out.Counts[r.Status]++
		if r.Status == tasks.StatusInProgress {
			out.InProgress = append(out.InProgress, tasks.InProgressTask{
				HarpID:  r.HarpID,
				Project: formatProjectLabel(r.ProjectDir, r.ProjectID),
			})
		}
	}
	return out
}

var statusesCmd = &cobra.Command{
	Use:   "statuses",
	Short: "List the task status taxonomy (name, order, terminal, requires_trigger)",
	Long: `List the canonical task statuses in display order, with metadata.

Lets a GUI render status groups and pickers from the source of truth instead of
hardcoding the status set. "terminal" marks completed statuses (Done/Archived);
"requires_trigger" marks statuses that need a revive condition (Deferred).

This is also the taxonomy the --status filter's @-classes are expanded from:
"@open" is every entry whose terminal bit is false, "@terminal" every entry
whose terminal bit is true. "@" is RESERVED as that class prefix, so it is
refused as the first character of a status name.`,
	Args: cobra.NoArgs,
	RunE: runStatusesCmd,
}

func runStatusesCmd(cmd *cobra.Command, args []string) error {
	statuses := tasks.Statuses()
	return cliemit.Emit(cmd, statuses, func() error {
		w := errwriter.New(cmd.OutOrStdout())
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
}

func init() {
	listCmd.Flags().StringSliceVar(&tasksListStatuses, "status", nil, `filter by status (repeatable); also accepts the classes "@open" (every non-terminal status) and "@terminal" (every completed one), expanded from the live taxonomy and mixable with literal statuses`)
	listCmd.Flags().StringVar(&tasksListTerm, "term", "", "filter by case-insensitive substring of task text")
	listCmd.Flags().StringVar(&tasksListTagQuery, "tag-query", "", `filter by postfix tag query, e.g. "urgent/release/and", "urgent/not" (see examples in --help; list tags with "taskloom tags")`)
	listCmd.Flags().BoolVar(&tasksListAll, "all", false, "include the tasks hidden by default: completed (Done/Archived) and Deferred")
	listCmd.Flags().BoolVar(&tasksListGlobal, "global", false, "aggregate tasks across every privately-homed project instead of just the current one (repo-homed projects are never included -- see this command's long help)")
	listCmd.Flags().StringVar(&tasksListSort, "sort", "", `sort order: "priority" for derived, rank-normalized priority (descending); default (unset) leaves today's order unchanged`)
	listCmd.Flags().BoolVar(&tasksListCompact, "compact", false, "emit compact rows (harp id, status, checked, tags, first-line headline) instead of full task bodies, for --format json/yaml/toml/markdown; ignored for the default text view, which is already one line per task")
	listCmd.Flags().IntVar(&tasksListLimit, "limit", 0, "cap the number of rows returned (0 = no cap); omitted rows are reported on stderr, and status/summary counts are never affected")

	addCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")
	addCmd.Flags().StringVar(&tasksAddTrigger, "trigger", "", "revive condition for a Deferred task (required when --status Deferred)")
	addCmd.Flags().StringArrayVar(&tasksAddTags, "tag", nil, "tag to set at creation, (namespace:)key(=value) (repeatable)")

	statusCmd.Flags().StringVar(&tasksStatusTrigger, "trigger", "", "revive condition when setting status to Deferred")

	tagCmd.Flags().StringArrayVar(&tasksTagAdd, "add", nil, "tag to add (repeatable)")
	tagCmd.Flags().StringArrayVar(&tasksTagRemove, "remove", nil, "tag to remove (repeatable)")

	tagsCmd.Flags().StringSliceVar(&tasksTagsStatuses, "status", nil, `count only tasks in these statuses (repeatable); accepts the "@open"/"@terminal" classes exactly as `+"`taskloom list --status`"+` does`)
	tagsCmd.Flags().StringVar(&tasksTagsTerm, "term", "", "count only tasks whose TEXT contains this case-insensitive substring (this narrows the tasks counted, never the tag names listed)")
	tagsCmd.Flags().StringVar(&tasksTagsTagQuery, "tag-query", "", `count only tasks matching this postfix tag query, e.g. "urgent/release/and" (see "taskloom list --help" for the grammar)`)
	summaryCmd.Flags().BoolVar(&tasksSummaryGlobal, "global", false, "sum the counts across every privately-homed project instead of just the current one (repo-homed projects are never included -- see \"taskloom list --help\")")

	tagsCmd.Flags().BoolVar(&tasksTagsGlobal, "global", false, "count across every privately-homed project instead of just the current one (repo-homed projects are never included -- see \"taskloom list --help\")")

	addCmd.SetFlagErrorFunc(textFlagError)
	editCmd.SetFlagErrorFunc(textFlagError)
	rootCmd.AddCommand(listCmd, addCmd, statusCmd, editCmd, tagCmd, tagsCmd, summaryCmd, statusesCmd)
}
