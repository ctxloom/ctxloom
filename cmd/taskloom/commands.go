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
	"github.com/ctxloom/ctxloom/internal/shared/iox"
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
  taskloom list --status "In Progress" --term docs

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
	// they're computed independently, straight from the store.
	Limit int

	// IncludeSummary asks the store for per-status counts alongside the rows.
	// `taskloom list` never sets it (`summary` is its own command); task_list's
	// include_summary does, and both go through one pipeline.
	IncludeSummary bool

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
	w := iox.NewErrWriter(out)
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
	w := iox.NewErrWriter(out)
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

// wrapTagQueryError adds the postfix-grammar hint to a malformed --tag-query,
// shared by both the single-project and --global list paths. tagma reports a
// malformed query as a plain error (no dedicated type to type-assert, unlike
// the retired pkg/tagquery.ParseError, whose message named the underflowing
// operator's operand count), so this checks for tasks.ErrTagQuery via
// errors.Is instead — the sentinel filterTasks wraps every tagma query
// error with — to tell "the --tag-query itself is bad" apart from an
// unrelated error (store I/O, project resolution) that shouldn't get the
// hint appended. The hint itself still names "operand" explicitly (not just
// tagma's own "stack underflow" wording) so a malformed query keeps failing
// loud with the problem named in the same vocabulary it always has,
// independent of the query engine underneath.
func wrapTagQueryError(err error) error {
	if err == nil || !errors.Is(err, tasks.ErrTagQuery) {
		return err
	}
	return fmt.Errorf("%w\nqueries are postfix: tags first, operator after — e.g. urgent/release/and, urgent/not; an and/or/not operator needs enough operands already on the query's stack, or it fails (see 'taskloom list --help' for more)", err)
}

// renderGlobalTaskTable prints a --global (or no-project-fallback) listing as
// one table section per project, reusing renderTaskTable per section so the
// per-row formatting matches the single-project view exactly. cfg is passed
// straight through to each section's renderTaskTable.
func renderGlobalTaskTable(out io.Writer, rows []taskRow, cfg tagma.HideConfig) error {
	w := iox.NewErrWriter(out)
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
type, or exact string to search for — "cloneMCPServer in internal/config",
"the Changed --json branch in cliemit.Resolve" — never "accessors.go:95".
Line numbers drift on every edit above them and are usually wrong by the
time anyone reads the task; a symbol name still finds it.`,
	Example: `  taskloom add "ship the release notes" --tag release --tag docs
  taskloom add "investigate flaky TestFoo" --status "In Progress"
  taskloom add "revisit caching" --status Deferred --trigger "the v2 API ships"
  taskloom add "dedupe the retry loop in the sync client
(found 2026-07-19, session icy-weary-chimp, while reviewing config layering)"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAdd,
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
		w := iox.NewErrWriter(cmd.OutOrStdout())
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
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, task.Text)
		return w.Err()
	})
}

var editCmd = &cobra.Command{
	Use:   "edit <harp-id> <text>",
	Short: "Replace a task's text in place (full new text)",
	Long: `Replace a task's text, keyed by its harp ID.

The entire text is replaced with what you pass (not patched); the task's
status and any Deferred trigger are left unchanged.`,
	Args: cobra.MinimumNArgs(2),
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
		w := iox.NewErrWriter(cmd.OutOrStdout())
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
	Short: "Add and/or remove flat tags on a task",
	Long: `Add and/or remove flat tags on a task, keyed by its harp ID.

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
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("%s\t%s\t%s\n", task.HarpID, task.Status, strings.Join(task.Tags, ","))
		return w.Err()
	})
}

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
	RunE: runTags,
}

func runTags(cmd *cobra.Command, args []string) error {
	tc, err := taskContextSingle()
	if err != nil {
		return err
	}
	res, err := operations.ListTagCounts(tc)
	if err != nil {
		return err
	}
	warnTask(res.Warning)
	return cliemit.Emit(cmd, res.Tags, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Project: %s\n\n", formatProjectLabel(res.ProjectDir, res.ProjectID))
		// The vocabulary enumeration goes through the same display-hide
		// filter as list/show: a tag hidden by tag_schema's tagma.hide:*
		// declarations shouldn't surface here either — see hideConfigFor.
		visible := visibleTagCounts(res.Tags, hideConfigFor(tc))
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
	})
}

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show per-status counts and active in-progress tasks",
	RunE:  runSummary,
}

func runSummary(cmd *cobra.Command, args []string) error {
	tc, err := taskContextSingle()
	if err != nil {
		return err
	}
	res, err := operations.ListTasks(tc, operations.ListOptions{IncludeSummary: true})
	if err != nil {
		return err
	}
	warnTask(res.Warning)
	sum := res.Summary
	return cliemit.Emit(cmd, sum, func() error {
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
	})
}

var statusesCmd = &cobra.Command{
	Use:   "statuses",
	Short: "List the task status taxonomy (name, order, terminal, requires_trigger)",
	Long: `List the canonical task statuses in display order, with metadata.

Lets a GUI render status groups and pickers from the source of truth instead of
hardcoding the status set. "terminal" marks completed statuses (Done/Archived);
"requires_trigger" marks statuses that need a revive condition (Deferred).`,
	Args: cobra.NoArgs,
	RunE: runStatusesCmd,
}

func runStatusesCmd(cmd *cobra.Command, args []string) error {
	statuses := tasks.Statuses()
	return cliemit.Emit(cmd, statuses, func() error {
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
	})
}

func init() {
	listCmd.Flags().StringSliceVar(&tasksListStatuses, "status", nil, "filter by status (repeatable)")
	listCmd.Flags().StringVar(&tasksListTerm, "term", "", "filter by case-insensitive substring of task text")
	listCmd.Flags().StringVar(&tasksListTagQuery, "tag-query", "", `filter by postfix tag query, e.g. "urgent/release/and", "urgent/not" (see examples in --help; list tags with "taskloom tags")`)
	listCmd.Flags().BoolVar(&tasksListAll, "all", false, "include the tasks hidden by default: completed (Done/Archived) and Deferred")
	listCmd.Flags().BoolVar(&tasksListGlobal, "global", false, "aggregate tasks across every privately-homed project instead of just the current one (repo-homed projects are never included -- see this command's long help)")
	listCmd.Flags().StringVar(&tasksListSort, "sort", "", `sort order: "priority" for derived, rank-normalized priority (descending); default (unset) leaves today's order unchanged`)
	listCmd.Flags().BoolVar(&tasksListCompact, "compact", false, "emit compact rows (harp id, status, checked, tags, first-line headline) instead of full task bodies, for --format json/yaml/toml/markdown; ignored for the default text view, which is already one line per task")
	listCmd.Flags().IntVar(&tasksListLimit, "limit", 0, "cap the number of rows returned (0 = no cap); omitted rows are reported on stderr, and status/summary counts are never affected")

	addCmd.Flags().StringVar(&tasksAddStatus, "status", "", "initial status (default: \"To Do\")")
	addCmd.Flags().StringVar(&tasksAddTrigger, "trigger", "", "revive condition for a Deferred task (required when --status Deferred)")
	addCmd.Flags().StringArrayVar(&tasksAddTags, "tag", nil, "flat tag to set at creation (repeatable)")

	statusCmd.Flags().StringVar(&tasksStatusTrigger, "trigger", "", "revive condition when setting status to Deferred")

	tagCmd.Flags().StringArrayVar(&tasksTagAdd, "add", nil, "tag to add (repeatable)")
	tagCmd.Flags().StringArrayVar(&tasksTagRemove, "remove", nil, "tag to remove (repeatable)")

	rootCmd.AddCommand(listCmd, addCmd, statusCmd, editCmd, tagCmd, tagsCmd, summaryCmd, statusesCmd)
}
