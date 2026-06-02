package operations

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectid"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/tasks"
)

// TaskContext carries the inputs a frontend gathers for a task operation: the
// project root, the project-id (empty to resolve live from the registry), and
// the active session harp (stamped as task provenance). Per ADR 0019 the
// frontend gathers these (git root, env); operations owns the resolution,
// migration, and store access.
type TaskContext struct {
	WorkDir     string
	ProjectID   string
	SessionHarp string
}

// TaskListResult is the rendered-agnostic result of ListTasks.
type TaskListResult struct {
	Path    string
	Tasks   []tasks.Task
	Summary *tasks.Summary
	Warning string // project-resolution notice (move/fork); the frontend surfaces it
}

// TaskResult is the result of a single-task mutation.
type TaskResult struct {
	Path    string
	Task    tasks.Task
	Warning string
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

// ListTasks resolves the project's task log and returns its tasks, optionally
// with a summary. Completed (Done/Archived) tasks are excluded by default;
// includeDone opts them back in, as does naming a status explicitly.
func ListTasks(tc TaskContext, statuses []string, term string, includeDone, includeSummary bool) (*TaskListResult, error) {
	store, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	list, err := store.List(statuses, term)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	// An append-only log accretes Done entries forever, so the default view
	// shows only live work. An explicit status filter is itself the opt-in (it
	// is honored verbatim by store.List), so only filter here when none was
	// given. The summary below still folds every task, so completed counts stay
	// visible.
	if !includeDone && len(statuses) == 0 {
		active := make([]tasks.Task, 0, len(list))
		for _, t := range list {
			if !t.Checked { // Checked == status is Done/Archived
				active = append(active, t)
			}
		}
		list = active
	}
	out := &TaskListResult{Path: store.Path(), Tasks: list, Warning: warning}
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
func AddTask(tc TaskContext, text, status string) (*TaskResult, error) {
	store, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.Add(text, status)
	if err != nil {
		return nil, fmt.Errorf("add task: %w", err)
	}
	return &TaskResult{Path: store.Path(), Task: task, Warning: warning}, nil
}

// SetTaskStatus moves a task to a different status, attributing the change to
// the acting session.
func SetTaskStatus(tc TaskContext, harpID, status string) (*TaskResult, error) {
	store, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.SetStatus(harpID, status)
	if err != nil {
		return nil, fmt.Errorf("set status: %w", err)
	}
	return &TaskResult{Path: store.Path(), Task: task, Warning: warning}, nil
}

// resolveTaskStore opens the per-project task log for tc: the project-id from
// tc (set by `ctxloom run`) or a live registry resolution, then one-time legacy
// migration, then OpenLog. The project-resolution warning is returned for the
// frontend to surface; it is never printed here.
func resolveTaskStore(tc TaskContext) (store *tasks.Store, warning string, err error) {
	projectID := tc.ProjectID
	if projectID == "" {
		pm, err := projectid.Open("")
		if err != nil {
			return nil, "", fmt.Errorf("open project registry: %w", err)
		}
		res, err := pm.Resolve(tc.WorkDir)
		if err != nil {
			return nil, "", fmt.Errorf("resolve project id: %w", err)
		}
		projectID = res.ProjectID
		warning = res.Warning
	}
	logPath, err := paths.TasksLogPath(projectID)
	if err != nil {
		return nil, warning, fmt.Errorf("task log path: %w", err)
	}
	migrateLegacyTasks(logPath, tc.WorkDir)
	store, err = tasks.OpenLog(logPath, tc.SessionHarp)
	if err != nil {
		return nil, warning, err
	}
	return store, warning, nil
}

// migrateLegacyTasks imports pre-ADR-0025 markdown stores (the legacy project
// file and any per-harp session stores) into the per-project log on first
// sight. A no-op once the log exists. Best-effort: a failure warns and the
// caller proceeds with whatever the log holds (CLAUDE.md fault tolerance).
func migrateLegacyTasks(logPath, workDir string) {
	if _, err := os.Stat(logPath); err == nil {
		return // log already exists; nothing to migrate
	}
	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		return
	}
	var harps []string
	if mgr, err := sessions.Open(""); err == nil {
		if entries, err := mgr.ListForProject(workDir); err == nil {
			for _, e := range entries {
				harps = append(harps, e.HarpName)
			}
		}
	}
	if err := tasks.MigrateLegacyIfNeeded(logPath, workDir, sessionsRoot, harps); err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: task migration: %v\n", err)
	}
}
