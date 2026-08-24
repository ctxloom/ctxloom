package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/priority"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
	taskloomconfig "github.com/ctxloom/ctxloom/internal/taskloom/config"
)

// noProjectNoticeFmt is the exact wording surfaced — on the CLI's stderr and
// in the MCP task_list result's Notice field — when a read falls back to
// every project because no project could be resolved from the working
// directory: no CTXLOOM_ROOT override, no enclosing git repo, and no
// established identity (registry entry or in-tree marker) for this exact
// path. %s is the working directory.
const noProjectNoticeFmt = "no project detected in %s (not a git repository, no CTXLOOM_ROOT override, no prior task history there) — showing tasks across all projects instead. Pass --project <id> (or cd into a project) to scope this."

// taskRow is one task in a listing that can span more than one project
// (--global, or the no-project fallback): the task itself plus the project
// it came from. The single-project path tags every row the same way, so a
// consumer never has to branch on whether a listing is global.
type taskRow struct {
	tasks.Task
	ProjectID  string `json:"project_id"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// compactTaskRow is taskRow's `compact` counterpart: a task's CompactTask
// presentation projection (see internal/shared/tasks.Task.Compact) plus the
// project it came from, so a --global/global=true compact listing keeps the
// same "every row is tagged with its project" shape the full-record taskRow
// gives a consumer — it never has to branch on whether a listing is global.
type compactTaskRow struct {
	tasks.CompactTask
	ProjectID  string `json:"project_id"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// compactRows projects a --global/global=true listing's taskRows to their
// compactTaskRow form, preserving each row's project attribution.
func compactRows(rows []taskRow) []compactTaskRow {
	out := make([]compactTaskRow, len(rows))
	for i, r := range rows {
		out[i] = compactTaskRow{CompactTask: r.Compact(), ProjectID: r.ProjectID, ProjectDir: r.ProjectDir}
	}
	return out
}

// composeGlobalNotice is the one disclosure a global listing carries: why the
// listing went global (empty for an explicit --global), then what --global
// structurally cannot see. Both surfaces render it, so neither drops a half.
func composeGlobalNotice(scope listScope, workDir string) string {
	limitation := globalScopeLimitationNote(workDir, rootCmd.PersistentFlags(), tasksHoming)
	if scope.Notice == "" {
		return limitation
	}
	return scope.Notice + "; " + limitation
}

// projectRows attributes list to one project, producing the row shape every
// structured listing emits, so a consumer sees the same keys either way.
func projectRows(list []tasks.Task, projectID, projectDir string) []taskRow {
	out := make([]taskRow, len(list))
	for i, t := range list {
		out[i] = taskRow{Task: t, ProjectID: projectID, ProjectDir: projectDir}
	}
	return out
}

// scopedListResult is everything a task listing decided, with no presentation
// choice made. Both list surfaces read it and neither re-decides any of it.
type scopedListResult struct {
	// Notice and ProjectCount are meaningful only when Global;
	// Path/ProjectID/ProjectDir/Summary/Warning only when not.
	Global       bool
	Notice       string
	ProjectCount int

	Path       string
	ProjectID  string
	ProjectDir string
	Summary    *tasks.Summary
	Warning    string

	// Rows carries per-row project attribution and is populated for both
	// scopes; Tasks is the unattributed view, single-project only.
	Rows  []taskRow
	Tasks []tasks.Task

	HiddenCompleted int
	HiddenDeferred  int
	OmittedByLimit  int
	PriorityWarning string

	// Filtered decides how the hidden-task counts are phrased.
	Filtered bool

	// TC is the context AFTER homing resolution — what a renderer must use for
	// tag-hiding config.
	TC operations.TaskContext
}

// listTasksScoped is the whole decision half of a task listing: scope, store,
// filters, priority, attribution. Everything left in either caller renders.
func listTasksScoped(tc operations.TaskContext, opts listOptions) (*scopedListResult, error) {
	if opts.Sort != "" && opts.Sort != sortPriority {
		return nil, fmt.Errorf("taskloom: unknown sort value %q (must be %q)", opts.Sort, sortPriority)
	}
	// The ONE place a status filter's @-classes are expanded, so every
	// surface that reaches a store through here — `taskloom list`, `taskloom
	// tags`, the task_list MCP tool — accepts them identically and none of
	// them re-derives which statuses a class covers. opts is a value, so the
	// rewrite is local to this listing.
	statuses, err := tasks.ExpandStatusClasses(opts.Statuses)
	if err != nil {
		return nil, err
	}
	opts.Statuses = statuses
	scope, err := resolveListScope(opts.Global, tc.ProjectID, tc.WorkDir, tc.WorkDirIsBoundary)
	if err != nil {
		return nil, err
	}
	if scope.Global {
		return listEveryProjectScoped(tc, opts, scope)
	}
	return listOneProjectScoped(tc, opts)
}

// listEveryProjectScoped aggregates every privately-homed project's store.
// ProjectID/ProjectDir stay empty — the rows span projects, so each carries
// its own attribution.
func listEveryProjectScoped(tc operations.TaskContext, opts listOptions, scope listScope) (*scopedListResult, error) {
	gres, err := listAllProjects(opts.Statuses, opts.Term, opts.TagQuery, opts.All,
		opts.Sort == sortPriority, tc.TagSchema, time.Now(), tc.SessionHarp, opts.Limit)
	if err != nil {
		return nil, wrapTagQueryError(err)
	}
	return &scopedListResult{
		Global:          true,
		Notice:          composeGlobalNotice(scope, tc.WorkDir),
		ProjectCount:    gres.ProjectCount,
		Rows:            gres.Rows,
		HiddenCompleted: gres.HiddenCompleted,
		HiddenDeferred:  gres.HiddenDeferred,
		OmittedByLimit:  gres.OmittedByLimit,
		PriorityWarning: gres.PriorityWarning,
		Filtered:        opts.Term != "" || opts.TagQuery != "",
		TC:              tc,
	}, nil
}

// listOneProjectScoped reads the single store tc resolves to, after homing.
func listOneProjectScoped(tc operations.TaskContext, opts listOptions) (*scopedListResult, error) {
	tc, err := resolveHoming(tc)
	if err != nil {
		return nil, err
	}
	res, err := operations.ListTasks(tc, operations.ListOptions{
		Statuses:       opts.Statuses,
		Term:           opts.Term,
		TagQuery:       opts.TagQuery,
		IncludeDone:    opts.All,
		IncludeSummary: opts.IncludeSummary,
		Limit:          opts.Limit,
	})
	if err != nil {
		return nil, wrapTagQueryError(err)
	}
	warnTask(res.Warning)
	out := &scopedListResult{
		Path:            res.Path,
		ProjectID:       res.ProjectID,
		ProjectDir:      res.ProjectDir,
		Summary:         res.Summary,
		Warning:         res.Warning,
		Tasks:           res.Tasks,
		HiddenCompleted: res.HiddenCompleted,
		HiddenDeferred:  res.HiddenDeferred,
		OmittedByLimit:  res.OmittedByLimit,
		Filtered:        opts.Term != "" || opts.TagQuery != "",
		TC:              tc,
	}
	if opts.Sort == sortPriority {
		results, diag, perr := operations.ComputeTaskPriorities(tc, time.Now())
		if perr != nil {
			return nil, fmt.Errorf("compute priorities: %w", perr)
		}
		attachPriority(out.Tasks, results)
		sortTasksByPriorityDesc(out.Tasks)
		out.PriorityWarning = priorityDiagnosticWarning(diag)
	}
	out.Rows = projectRows(out.Tasks, res.ProjectID, res.ProjectDir)
	return out, nil
}

// listScope is the resolved scope for a task-listing read: either the one
// project taskContext() would resolve (the default) or every project
// (--global/all_projects, or the no-project fallback). Notice is non-empty
// only for the fallback case — an explicit --global is a silent opt-in, it
// needs no explanation.
type listScope struct {
	Global bool
	Notice string
}

// resolveListScope decides whether a task-listing read should be scoped to
// the current project or aggregate every project.
//
// explicit is the --global / all_projects request: when true, it always
// wins and the read aggregates every project, no questions asked.
//
// Otherwise the read stays project-scoped when any of, in order: a pinned
// project-id is already in play (--project or the CTXLOOM_PROJECT_ID a
// session exports); the working directory sits inside a real project
// boundary (CTXLOOM_ROOT or an enclosing git repo — including a git repo's
// very first taskloom call, which is still a real project); or the working
// directory already carries an established identity (a registry entry or an
// in-tree marker from some earlier call, even without git).
//
// None of those holding means there genuinely is no project in play — cwd is
// just wherever the shell happened to be — so the read defaults to global
// rather than silently minting a throwaway project identity for an arbitrary
// directory. That fallback carries a Notice explaining why.
//
// workDirIsBoundary is that middle test, DECIDED BY THE CALLER: it is the
// boundary half of the very resolution that produced workDir
// (operations.TaskContext.WorkDirIsBoundary, set by taskContext). Re-deriving
// it here would resolve the working directory a second time per listing and
// let the scope decision be made against a different answer than the store
// was opened with.
func resolveListScope(explicit bool, pinnedProjectID, workDir string, workDirIsBoundary bool) (listScope, error) {
	if explicit {
		return listScope{Global: true}, nil
	}
	if pinnedProjectID != "" {
		return listScope{}, nil
	}
	if workDirIsBoundary {
		return listScope{}, nil
	}
	established, err := isEstablishedProject(workDir)
	if err != nil {
		return listScope{}, err
	}
	if established {
		return listScope{}, nil
	}
	return listScope{Global: true, Notice: fmt.Sprintf(noProjectNoticeFmt, workDir)}, nil
}

// isEstablishedProject reports whether workDir already carries a project
// identity — a registry entry keyed on this exact path, or an in-tree
// project-id marker — without minting one. A read must never conjure a new
// project identity for a directory just because it was pointed at one; only
// mutations (add/status/tag/edit) do that, and this function is never on
// their path.
func isEstablishedProject(workDir string) (bool, error) {
	pm, err := projectid.Open("")
	if err != nil {
		return false, fmt.Errorf("open project registry: %w", err)
	}
	if e, err := pm.ResolveByPath(workDir); err != nil {
		return false, err
	} else if e != nil {
		return true, nil
	}
	marker, err := projectid.ReadMarker(workDir)
	if err != nil {
		return false, err
	}
	return marker != "", nil
}

// globalScopeGeneralLimitation is the caveat every --global / global=true
// listing carries: listAllProjects can only discover PRIVATELY-homed stores
// under ~/.ctxloom/tasks (paths.HomeTasksDir) by walking that directory.
// Nothing registers a repo-homed store's location anywhere global — a
// project configured `homing: repo` mints no project-id and consults no
// registry at all (see paths.ModeRepo's doc) — so listAllProjects has no way
// to find one even in principle. An aggregation that silently under-reports
// is worse than one that declares its scope: every --global
// read carries this note rather than presenting an incomplete answer as if
// it were complete.
const globalScopeGeneralLimitation = "--global lists privately-homed projects only (under ~/.ctxloom/tasks); a repo-homed project (homing: repo, its log checked into <repo>/.taskloom/tasks.jsonl) is not registered anywhere global and cannot be included"

// globalScopeLimitationNote is globalScopeGeneralLimitation, sharpened when
// workDir itself resolves to a repo-homed project — the case most likely to
// bite: a user standing INSIDE a repo-homed project who asks --global for
// "everything" would otherwise get a confident answer that silently excludes
// their own current project's tasks. workDir's mode resolution uses the same
// taskloomconfig.ResolveMode chain resolveHoming would (fs/flagValue let the
// caller pass rootCmd's --homing plumbing); any error resolving it is
// swallowed and falls back to the general note — this is a best-effort
// disclosure, never something that should block or fail a listing.
func globalScopeLimitationNote(workDir string, fs *pflag.FlagSet, homingFlagValue string) string {
	if workDir == "" {
		return globalScopeGeneralLimitation
	}
	mode, err := taskloomconfig.ResolveMode(workDir, fs, homingFlagValue)
	if err != nil || mode != paths.ModeRepo {
		return globalScopeGeneralLimitation
	}
	return fmt.Sprintf(
		"this project (%s) is repo-homed (its tasks are checked into %s) -- --global cannot see it: only privately-homed projects under ~/.ctxloom/tasks are aggregated",
		workDir, filepath.Join(workDir, paths.RepoDirName, paths.RepoTasksFileName))
}

// globalListResult is the render-agnostic result of listing across every
// project: one row per visible task tagged with its project, the number of
// project stores scanned, and the same hidden-completed/hidden-deferred
// counts operations.ListTasks reports per project, summed.
type globalListResult struct {
	Rows            []taskRow
	ProjectCount    int
	HiddenCompleted int
	HiddenDeferred  int

	// PriorityWarning is non-empty when sortPriority was requested and at
	// least one scanned project's ranking came back MEANINGLESS (see
	// priority.Diagnostics) — one line per affected project, joined. Empty
	// when sortPriority wasn't requested, or every scanned project's ranking
	// was fine.
	PriorityWarning string

	// OmittedByLimit counts the rows a positive limit cut off Rows after
	// every project was scanned, filtered, and sorted — the global-scope
	// counterpart of operations.TaskListResult.OmittedByLimit. Previously
	// limit was silently ignored entirely whenever a listing went global
	// (explicit --global or the no-project fallback): the caller got back
	// every row from every project with no cap and no signal that anything
	// was supposed to have been capped.
	OmittedByLimit int
}

// listAllProjects aggregates every project's task log under ~/.ctxloom/tasks
// (one <project-id>.jsonl per project), applying the same
// status/term/tag-query filter and default active-only view
// operations.ListTasks applies for a single project. Rows are
// grouped by project-id (renderGlobalTaskTable relies on that grouping being
// consecutive); WITHIN a group they're ordered by harp-id, or — when
// sortPriority is set — by derived priority descending instead. Priority is
// computed against each project's own FULL current snapshot (schema shared
// across every project, since a --global read has only one resolved
// tag_schema to apply — see cmd/taskloom/root.go's resolveTagSchema),
// mirroring operations.ComputeTaskPriorities' single-project scoping
// decision: percentile rank is a property of each project's whole
// non-terminal population, not of whatever page the term/tag-query/status
// filter narrowed that project's rows to.
//
// A project grouping is preserved even when sorting by priority — a fully
// cross-project intermixed sort would need renderGlobalTaskTable's
// consecutive-project-id grouping to change too; scoping the reorder to
// within each project's own section is a deliberate, bounded choice, not an
// oversight.
//
// A tasks dir that doesn't exist yet (nothing has ever been tracked
// anywhere) is zero projects, zero tasks — not an error.
func listAllProjects(statuses []string, term, tagQuery string, includeDone, byPriority bool, schema *tagschema.Schema, now time.Time, sessionHarp string, limit int) (*globalListResult, error) {
	logs, err := projectLogPaths()
	if err != nil {
		return nil, err
	}

	scan := globalScan{
		statuses:    statuses,
		term:        term,
		tagQuery:    tagQuery,
		includeDone: includeDone,
		byPriority:  byPriority,
		schema:      schema,
		now:         now,
		sessionHarp: sessionHarp,
	}

	out := &globalListResult{}
	var priorityWarnings []string
	for _, log := range logs {
		p, err := scan.project(log.path, log.projectID)
		if err != nil {
			return nil, err
		}
		out.ProjectCount++
		out.HiddenCompleted += p.hiddenCompleted
		out.HiddenDeferred += p.hiddenDeferred
		if p.priorityWarning != "" {
			priorityWarnings = append(priorityWarnings, p.priorityWarning)
		}
		for _, t := range p.visible {
			out.Rows = append(out.Rows, taskRow{Task: t, ProjectID: log.projectID})
		}
	}
	// Joined on newlines, not "; ": each project's warning is already
	// multi-line, and running them together on one line produces a
	// paragraph no reader parses.
	out.PriorityWarning = strings.Join(priorityWarnings, "\n")
	sortGlobalRows(out.Rows, byPriority)
	// Applied LAST, after every project has been scanned/filtered/sorted:
	// limit caps the TOTAL row count across all projects, mirroring the
	// single-project path (operations.ListTasks) exactly.
	out.Rows, out.OmittedByLimit = capRows(out.Rows, limit)
	return out, nil
}

// projectLog is one privately-homed project's task log: the file to open and
// the project-id its filename encodes.
type projectLog struct {
	path      string
	projectID string
}

// projectLogPaths enumerates every privately-homed project's task log under
// ~/.ctxloom/tasks (one <project-id>.jsonl per project). A tasks dir that
// does not exist yet — nothing has ever been tracked anywhere — is zero
// projects, not an error.
func projectLogPaths() ([]projectLog, error) {
	dir, err := paths.HomeTasksDir()
	if err != nil {
		return nil, fmt.Errorf("tasks dir: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}
	var out []projectLog
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), paths.TasksLogExt) {
			continue
		}
		out = append(out, projectLog{
			path:      filepath.Join(dir, e.Name()),
			projectID: strings.TrimSuffix(e.Name(), paths.TasksLogExt),
		})
	}
	return out, nil
}

// capRows truncates rows to limit and reports how many it dropped. A limit
// <= 0 means no cap, and nothing is ever dropped silently — the count is what
// the caller surfaces as omitted_by_limit.
func capRows(rows []taskRow, limit int) (capped []taskRow, omitted int) {
	if limit <= 0 || len(rows) <= limit {
		return rows, 0
	}
	return rows[:limit], len(rows) - limit
}

// globalScan is the per-project half of a --global listing's inputs: the
// filters, the shared tag-schema, and the clock. It exists so one project's
// scan can be read on its own rather than as nine arguments threaded through
// listAllProjects' loop body.
type globalScan struct {
	statuses    []string
	term        string
	tagQuery    string
	includeDone bool
	byPriority  bool
	schema      *tagschema.Schema
	now         time.Time
	sessionHarp string
}

// projectScan is one project store's contribution to a --global listing.
type projectScan struct {
	visible                         []tasks.Task
	hiddenCompleted, hiddenDeferred int
	priorityWarning                 string
}

// project opens one project's log at logPath and applies the scan's filters
// to it. Every error names the project, because a --global read touches many
// and "list tasks: ..." alone would not say which one failed.
func (g globalScan) project(logPath, projectID string) (projectScan, error) {
	store, err := tasks.OpenLog(logPath, g.sessionHarp)
	if err != nil {
		return projectScan{}, fmt.Errorf("open project %s: %w", projectID, err)
	}
	list, err := store.ListWithTagQuery(g.statuses, g.term, g.tagQuery, g.schema)
	if err != nil {
		return projectScan{}, fmt.Errorf("list project %s: %w", projectID, err)
	}
	visible, hiddenCompleted, hiddenDeferred := filterActiveDefault(list, g.includeDone, g.statuses)
	out := projectScan{visible: visible, hiddenCompleted: hiddenCompleted, hiddenDeferred: hiddenDeferred}
	if !g.byPriority {
		return out, nil
	}
	warning, err := g.rank(store, projectID, visible)
	if err != nil {
		return projectScan{}, err
	}
	out.priorityWarning = warning
	return out, nil
}

// rank attaches derived priorities to visible, computed against the project's
// OWN full snapshot rather than the filtered page — percentile rank is a
// property of that project's whole non-terminal population. The returned
// string is the degenerate-ranking warning, attributed to the project, or
// empty when the ranking is healthy.
func (g globalScan) rank(store *tasks.Store, projectID string, visible []tasks.Task) (string, error) {
	all, err := store.Snapshot()
	if err != nil {
		return "", fmt.Errorf("snapshot project %s for priority computation: %w", projectID, err)
	}
	results, diag, err := priority.Compute(all, g.schema, g.now)
	if err != nil {
		return "", fmt.Errorf("compute priorities for project %s: %w", projectID, err)
	}
	attachPriority(visible, results)
	return attributeToProject(projectID, priorityDiagnosticWarning(diag)), nil
}

// attributeToProject prefixes EVERY line of a warning with the project it
// came from. A --global listing interleaves several projects' findings, and
// the diagnostic is multi-line (one line per thinly-covered formula term),
// so attributing only the first line would leave every line after it
// looking like a finding about whichever project was named last.
func attributeToProject(projectID, warning string) string {
	if warning == "" {
		return ""
	}
	lines := strings.Split(warning, "\n")
	for i, line := range lines {
		lines[i] = fmt.Sprintf("project %s: %s", projectID, line)
	}
	return strings.Join(lines, "\n")
}

// sortGlobalRows groups rows by project-id (renderGlobalTaskTable relies on
// that grouping being consecutive) and orders each group by harp-id, or by
// derived priority descending when the listing asked for it.
func sortGlobalRows(rows []taskRow, byPriority bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ProjectID != rows[j].ProjectID {
			return rows[i].ProjectID < rows[j].ProjectID
		}
		if byPriority {
			return priorityOf(rows[i].Task) > priorityOf(rows[j].Task)
		}
		return rows[i].HarpID < rows[j].HarpID
	})
}

// filterActiveDefault reproduces operations.ListTasks's default
// active-only view for a raw task list already filtered by status/term/tag: a
// completed (Done/Archived) or Deferred task is hidden unless includeDone is
// set or an explicit status filter was already given (that filter is itself
// the opt-in, honored verbatim by the store). Duplicated here — rather than
// exported from internal/shared/tasks/operations — because a --global
// aggregation opens each project's store directly via tasks.OpenLog and must
// apply the identical rule per project; operations.ListTasks
// itself only ever resolves exactly one project.
func filterActiveDefault(list []tasks.Task, includeDone bool, statuses []string) (visible []tasks.Task, hiddenCompleted, hiddenDeferred int) {
	if includeDone || len(statuses) > 0 {
		return list, 0, 0
	}
	visible = make([]tasks.Task, 0, len(list))
	for _, t := range list {
		switch {
		case t.Checked:
			hiddenCompleted++
		case t.Status == tasks.StatusDeferred:
			hiddenDeferred++
		default:
			visible = append(visible, t)
		}
	}
	return visible, hiddenCompleted, hiddenDeferred
}
