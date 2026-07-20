// Package operations is the frontend-agnostic layer between the task store and
// its frontends (the CLI, the MCP server, and ctxloom's run integration). Per
// ADR 0019 the frontend gathers the inputs (git root, env); operations owns
// project resolution and store access.
package operations

import (
	"fmt"
	"sort"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
)

// TaskContext carries the inputs a frontend gathers for a task operation: the
// project root, the project-id (empty to resolve live from the registry), and
// the active session harp (stamped as task provenance).
type TaskContext struct {
	WorkDir     string
	ProjectID   string
	SessionHarp string

	// HomingMode selects where the project's task-store log lives —
	// paths.ModeHome (private, under ~/.ctxloom/tasks, keyed by a
	// minted/registered project-id) or paths.ModeRepo (checked into the
	// project tree at WorkDir/.taskloom/tasks.jsonl, no project-id involved).
	// The zero value "" behaves exactly like paths.ModeHome — today's sole
	// behavior — so every caller that predates homing-mode selection (every
	// existing internal/cli, internal/operations, and internal/lm/isolation
	// call site) keeps working completely unchanged. cmd/taskloom's own
	// frontend resolves this explicitly via internal/taskloom/config.
	// ResolveMode, which resolves to the SAME paths.ModeHome default when
	// neither a config file nor a flag decides it — see that package's doc
	// for why ModeHome (the pre-homing status quo) is the one safe silent
	// default, while ModeRepo is only ever chosen explicitly.
	HomingMode paths.Mode
}

// TaskListResult is the render-agnostic result of ListTasks.
type TaskListResult struct {
	Path    string
	Tasks   []tasks.Task
	Summary *tasks.Summary
	Warning string // project-resolution notice (move/fork); the frontend surfaces it

	// HiddenCompleted/HiddenDeferred count the tasks that matched every
	// requested filter but were dropped by the default active-only view
	// (completed = Done/Archived, i.e. Checked). Zero when includeDone or an
	// explicit status filter disabled that view. Frontends use these so a
	// filtered listing never silently truncates its answer — a user who
	// tag-queries a vocabulary they applied to now-finished work would
	// otherwise see matches vanish with no trace.
	HiddenCompleted int
	HiddenDeferred  int

	// ProjectID/ProjectDir identify the store the listing came from. In
	// multi-root workspaces (several .ctxloom trees under one repo) the
	// resolved project is genuinely ambiguous to the user — frontends show
	// these so a listing always names its source.
	ProjectID  string
	ProjectDir string // registered project root; empty when not registered
}

// TaskResult is the result of a single-task mutation.
type TaskResult struct {
	Path    string
	Task    tasks.Task
	Warning string

	// ProjectID/ProjectDir identify the store the mutation landed in — a
	// pinned project-id (CTXLOOM_PROJECT_ID exported by `ctxloom run`) wins
	// over the working directory, so without this a task added from another
	// project's tree lands somewhere invisible to the user.
	ProjectID  string
	ProjectDir string
}

// ResolveProjectIdentity resolves the stable project-id for workDir, minting
// and marking a fresh identity on first sight. Returns the id and any move/fork
// notice for the frontend to surface. Used by `ctxloom run` pre-launch to
// export CTXLOOM_PROJECT_ID into the session environment.
func ResolveProjectIdentity(workDir string) (projectID, warning string, err error) {
	pm, err := projectid.Open("")
	if err != nil {
		return "", "", err
	}
	res, err := pm.Resolve(workDir)
	if err != nil {
		return "", "", err
	}
	return res.ProjectID, res.Warning, nil
}

// ResolveLogPath resolves the per-project task log path for tc — the project-id
// pinned in tc (by `ctxloom run`) or a live registry resolution — without
// opening the store. `taskloom watch` uses it to learn which file to watch, so
// the path convention stays owned here rather than reconstructed by a frontend.
//
// In ModeRepo, projectID is always returned empty: the repo IS the identity
// in that mode (see paths.TasksLogPath and internal/taskloom/config's doc),
// so there is no id to mint or resolve.
func ResolveLogPath(tc TaskContext) (projectID, logPath string, err error) {
	if tc.HomingMode == paths.ModeRepo {
		logPath, err = paths.TasksLogPath(paths.ModeRepo, tc.WorkDir, "")
		if err != nil {
			return "", "", fmt.Errorf("task log path: %w", err)
		}
		return "", logPath, nil
	}
	projectID = tc.ProjectID
	if projectID == "" {
		pm, perr := projectid.Open("")
		if perr != nil {
			return "", "", fmt.Errorf("open project registry: %w", perr)
		}
		res, rerr := pm.Resolve(tc.WorkDir)
		if rerr != nil {
			return "", "", fmt.Errorf("resolve project id: %w", rerr)
		}
		projectID = res.ProjectID
	}
	logPath, err = paths.TasksLogPath(paths.ModeHome, "", projectID)
	if err != nil {
		return projectID, "", fmt.Errorf("task log path: %w", err)
	}
	return projectID, logPath, nil
}

// ListTasks resolves the project's task log and returns its tasks, optionally
// with a summary. Completed (Done/Archived) tasks are excluded by default;
// includeDone opts them back in, as does naming a status explicitly.
func ListTasks(tc TaskContext, statuses []string, term string, includeDone, includeSummary bool) (*TaskListResult, error) {
	return listTasks(tc, statuses, term, "", includeDone, includeSummary)
}

// ListTasksWithTagQuery is ListTasks with an additional postfix tag-query
// filter (see pkg/tagquery for the grammar, e.g. "urgent/release/and"). An
// empty tagQuery behaves exactly like ListTasks. A malformed tagQuery
// surfaces as an error — never a silently empty or unfiltered result.
func ListTasksWithTagQuery(tc TaskContext, statuses []string, term, tagQuery string, includeDone, includeSummary bool) (*TaskListResult, error) {
	return listTasks(tc, statuses, term, tagQuery, includeDone, includeSummary)
}

func listTasks(tc TaskContext, statuses []string, term, tagQuery string, includeDone, includeSummary bool) (*TaskListResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	list, err := store.ListWithTagQuery(statuses, term, tagQuery)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	// An append-only log accretes Done entries forever, so the default view
	// shows only live work. An explicit status filter is itself the opt-in (it
	// is honored verbatim by store.List), so only filter here when none was
	// given. The summary below still folds every task, so completed counts stay
	// visible. Deferred tasks are parked on a trigger and are likewise hidden
	// from the active view — surface them with `--status Deferred` (or the
	// check-triggers command), or with includeDone.
	var hiddenCompleted, hiddenDeferred int
	if !includeDone && len(statuses) == 0 {
		active := make([]tasks.Task, 0, len(list))
		for _, t := range list {
			switch {
			case t.Checked:
				hiddenCompleted++
			case t.Status == tasks.StatusDeferred:
				hiddenDeferred++
			default:
				active = append(active, t)
			}
		}
		list = active
	}
	out := &TaskListResult{Path: store.Path(), Tasks: list, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir,
		HiddenCompleted: hiddenCompleted, HiddenDeferred: hiddenDeferred}
	if includeSummary {
		sum, err := store.Summarize()
		if err != nil {
			return nil, fmt.Errorf("summarize: %w", err)
		}
		out.Summary = &sum
	}
	return out, nil
}

// AddTask appends a task to the project log, stamping the session as origin.
// A non-empty trigger parks the task on a revive condition; required when
// status is Deferred (the store enforces the invariant for both CLI and MCP).
func AddTask(tc TaskContext, text, status, trigger string) (*TaskResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.AddWithTrigger(text, status, trigger)
	if err != nil {
		return nil, fmt.Errorf("add task: %w", err)
	}
	return &TaskResult{Path: store.Path(), Task: task, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir}, nil
}

// AddTaskWithTags is AddTask with an initial tag set stamped on the same
// `add` event.
func AddTaskWithTags(tc TaskContext, text, status, trigger string, tags []string) (*TaskResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.AddWithTags(text, status, trigger, tags...)
	if err != nil {
		return nil, fmt.Errorf("add task: %w", err)
	}
	return &TaskResult{Path: store.Path(), Task: task, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir}, nil
}

// TagTask adds and/or removes tags on an existing task in one call,
// attributing the change to the acting session. At least one of add/remove
// must be non-empty. Add is applied before remove, so a tag named in both
// lists ends up removed.
func TagTask(tc TaskContext, harpID string, add, remove []string) (*TaskResult, error) {
	if len(add) == 0 && len(remove) == 0 {
		return nil, fmt.Errorf("at least one tag to add or remove is required")
	}
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	var task tasks.Task
	if len(add) > 0 {
		task, err = store.AddTags(harpID, add...)
		if err != nil {
			return nil, fmt.Errorf("add tags: %w", err)
		}
	}
	if len(remove) > 0 {
		task, err = store.RemoveTags(harpID, remove...)
		if err != nil {
			return nil, fmt.Errorf("remove tags: %w", err)
		}
	}
	return &TaskResult{Path: store.Path(), Task: task, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir}, nil
}

// SetTaskStatus moves a task to a different status, attributing the change to
// the acting session. A non-empty trigger (re)sets the revive condition;
// moving to Deferred requires one (supplied here or already on the task).
func SetTaskStatus(tc TaskContext, harpID, status, trigger string) (*TaskResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.SetStatusWithTrigger(harpID, status, trigger)
	if err != nil {
		return nil, fmt.Errorf("set status: %w", err)
	}
	return &TaskResult{Path: store.Path(), Task: task, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir}, nil
}

// EditTask replaces a task's text in place, keyed by harp ID, attributing the
// edit to the acting session. The whole text is replaced; status and trigger
// are untouched.
func EditTask(tc TaskContext, harpID, text string) (*TaskResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.SetText(harpID, text)
	if err != nil {
		return nil, fmt.Errorf("edit task: %w", err)
	}
	return &TaskResult{Path: store.Path(), Task: task, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir}, nil
}

// TagCount reports one tag's usage across the project's tasks. Active counts
// the tasks visible in the default list view (not completed, not Deferred);
// Total counts every task carrying the tag, however parked or finished.
// Showing both is what makes the number self-explanatory: a tag with
// "0 active, 40 total" is a finished workstream, not a typo.
type TagCount struct {
	Tag    string `json:"tag"`
	Active int    `json:"active"`
	Total  int    `json:"total"`
}

// TagListResult is the render-agnostic result of ListTagCounts.
type TagListResult struct {
	Path    string
	Tags    []TagCount
	Warning string

	ProjectID  string
	ProjectDir string
}

// ListTagCounts enumerates the tags currently in use across ALL of the
// project's tasks (including completed and Deferred ones), with per-tag
// counts, sorted by tag name. This is the read side of the tag vocabulary:
// tags are free-form on write, so without an enumeration surface the
// vocabulary is write-only and typo-twins accumulate invisibly.
func ListTagCounts(tc TaskContext) (*TagListResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	list, err := store.List(nil, "")
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	counts := make(map[string]*TagCount)
	for _, t := range list {
		active := !t.Checked && t.Status != tasks.StatusDeferred
		for _, tag := range t.Tags {
			c := counts[tag]
			if c == nil {
				c = &TagCount{Tag: tag}
				counts[tag] = c
			}
			c.Total++
			if active {
				c.Active++
			}
		}
	}
	out := make([]TagCount, 0, len(counts))
	for _, c := range counts {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return &TagListResult{Path: store.Path(), Tags: out, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir}, nil
}

// DeferredSince resolves the project's task store and returns, for every
// currently Deferred task, the timestamp it most recently entered that
// status. Trigger evaluation uses this to scope its evidence per task; it is
// otherwise the same TaskContext-resolution wrapper as ListTasks/AddTask.
func DeferredSince(tc TaskContext) (map[string]time.Time, error) {
	store, _, _, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	since, err := store.DeferredSince()
	if err != nil {
		return nil, fmt.Errorf("deferred since: %w", err)
	}
	return since, nil
}

// projectIdentity names the project a task operation resolved to, so
// frontends can show which store they acted on — in multi-root workspaces
// (several .ctxloom trees under one repo) the resolved project is genuinely
// ambiguous to the user.
type projectIdentity struct {
	ID  string
	Dir string // registered project root; empty when not registered
}

// resolveTaskStore opens the project's task log for tc: in ModeRepo, the
// checked-in log inside tc.WorkDir (resolveRepoHomedStore); otherwise
// (ModeHome, or "" — every pre-homing caller) the project-id from tc (set by
// `ctxloom run`) or a live registry resolution, then OpenLog. The
// project-resolution warning is returned for the frontend to surface; it is
// never printed here.
func resolveTaskStore(tc TaskContext) (store *tasks.Store, proj projectIdentity, warning string, err error) {
	if tc.HomingMode == paths.ModeRepo {
		return resolveRepoHomedStore(tc)
	}
	proj.ID = tc.ProjectID
	pm, pmErr := projectid.Open("")
	if proj.ID == "" {
		if pmErr != nil {
			return nil, proj, "", fmt.Errorf("open project registry: %w", pmErr)
		}
		res, rerr := pm.Resolve(tc.WorkDir)
		if rerr != nil {
			return nil, proj, "", fmt.Errorf("resolve project id: %w", rerr)
		}
		proj.ID = res.ProjectID
		warning = res.Warning
	}
	// Best-effort: name the registered root for the id so frontends can show
	// where the store lives. A registry miss leaves Dir empty — never fatal.
	if pmErr == nil {
		if e, lerr := pm.ResolveByID(proj.ID); lerr == nil && e != nil {
			proj.Dir = e.Path
		}
		// A pinned project-id silently wins over the working directory. When
		// the cwd demonstrably belongs to a DIFFERENT project (its marker or
		// registry entry says so), say it — this is exactly how tasks end up
		// filed under the wrong project from a session that cd'd elsewhere.
		if tc.ProjectID != "" && tc.WorkDir != "" {
			cwdID, _ := projectid.ReadMarker(tc.WorkDir)
			if cwdID == "" {
				if e, lerr := pm.ResolveByPath(tc.WorkDir); lerr == nil && e != nil {
					cwdID = e.ProjectID
				}
			}
			if cwdID != "" && cwdID != proj.ID {
				note := fmt.Sprintf("acting on pinned project %s, but %s belongs to project %s — pass --project %s to target it",
					proj.ID, tc.WorkDir, cwdID, cwdID)
				if warning != "" {
					warning += "; " + note
				} else {
					warning = note
				}
			}
		}
	}
	logPath, err := paths.TasksLogPath(paths.ModeHome, "", proj.ID)
	if err != nil {
		return nil, proj, warning, fmt.Errorf("task log path: %w", err)
	}
	store, err = tasks.OpenLog(logPath, tc.SessionHarp)
	if err != nil {
		return nil, proj, warning, err
	}
	return store, proj, warning, nil
}

// resolveRepoHomedStore opens the REPO-HOMED task store: the log lives
// checked into the project tree at tc.WorkDir/paths.RepoDirName/
// paths.RepoTasksFileName (.taskloom/tasks.jsonl), alongside
// .taskloom/config.yaml. The repo IS the identity in this mode — no
// project-id is minted or consulted (see paths.TasksLogPath's doc and
// internal/taskloom/config's package doc for the full justification).
// tc.WorkDir is expected to already be redirected through the
// worktree-to-primary-checkout boundary by the caller (workdir.
// ResolveBoundary via projectroot.TaskStoreRoot), so a linked worktree lands
// on the SAME log a primary-checkout invocation would — exactly like
// ModeHome's project-id resolution already does today.
func resolveRepoHomedStore(tc TaskContext) (store *tasks.Store, proj projectIdentity, warning string, err error) {
	if tc.WorkDir == "" {
		return nil, proj, "", fmt.Errorf("repo-homed task store: no project root resolved (working directory unavailable)")
	}
	proj.Dir = tc.WorkDir
	if tc.ProjectID != "" {
		warning = fmt.Sprintf("--project %s is ignored in repo-homed mode; %s is the store's identity", tc.ProjectID, tc.WorkDir)
	}
	logPath, err := paths.TasksLogPath(paths.ModeRepo, tc.WorkDir, "")
	if err != nil {
		return nil, proj, warning, fmt.Errorf("task log path: %w", err)
	}
	store, err = tasks.OpenLog(logPath, tc.SessionHarp)
	if err != nil {
		return nil, proj, warning, err
	}
	return store, proj, warning, nil
}
