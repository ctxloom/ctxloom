package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/taskloom/workdir"
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
func resolveListScope(explicit bool, pinnedProjectID, workDir string) (listScope, error) {
	if explicit {
		return listScope{Global: true}, nil
	}
	if pinnedProjectID != "" {
		return listScope{}, nil
	}
	if _, found := workdir.ResolveBoundary(); found {
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

// globalListResult is the render-agnostic result of listing across every
// project: one row per visible task tagged with its project, the number of
// project stores scanned, and the same hidden-completed/hidden-deferred
// counts operations.ListTasksWithTagQuery reports per project, summed.
type globalListResult struct {
	Rows            []taskRow
	ProjectCount    int
	HiddenCompleted int
	HiddenDeferred  int
}

// listAllProjects aggregates every project's task log under ~/.ctxloom/tasks
// (one <project-id>.jsonl per project), applying the same
// status/term/tag-query filter and default active-only view
// operations.ListTasksWithTagQuery applies for a single project. Rows are
// sorted by project-id, then by harp-id within a project, so the output is
// deterministic. A tasks dir that doesn't exist yet (nothing has ever been
// tracked anywhere) is zero projects, zero tasks — not an error.
func listAllProjects(statuses []string, term, tagQuery string, includeDone bool, sessionHarp string) (*globalListResult, error) {
	dir, err := paths.HomeTasksDir()
	if err != nil {
		return nil, fmt.Errorf("tasks dir: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &globalListResult{}, nil
		}
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}

	out := &globalListResult{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), paths.TasksLogExt) {
			continue
		}
		projectID := strings.TrimSuffix(e.Name(), paths.TasksLogExt)
		store, err := tasks.OpenLog(filepath.Join(dir, e.Name()), sessionHarp)
		if err != nil {
			return nil, fmt.Errorf("open project %s: %w", projectID, err)
		}
		list, err := store.ListWithTagQuery(statuses, term, tagQuery)
		if err != nil {
			return nil, fmt.Errorf("list project %s: %w", projectID, err)
		}
		visible, hiddenCompleted, hiddenDeferred := filterActiveDefault(list, includeDone, statuses)
		out.ProjectCount++
		out.HiddenCompleted += hiddenCompleted
		out.HiddenDeferred += hiddenDeferred
		for _, t := range visible {
			out.Rows = append(out.Rows, taskRow{Task: t, ProjectID: projectID})
		}
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		if out.Rows[i].ProjectID != out.Rows[j].ProjectID {
			return out.Rows[i].ProjectID < out.Rows[j].ProjectID
		}
		return out.Rows[i].HarpID < out.Rows[j].HarpID
	})
	return out, nil
}

// filterActiveDefault reproduces operations.ListTasksWithTagQuery's default
// active-only view for a raw task list already filtered by status/term/tag: a
// completed (Done/Archived) or Deferred task is hidden unless includeDone is
// set or an explicit status filter was already given (that filter is itself
// the opt-in, honored verbatim by the store). Duplicated here — rather than
// exported from internal/shared/tasks/operations — because a --global
// aggregation opens each project's store directly via tasks.OpenLog and must
// apply the identical rule per project; operations.ListTasksWithTagQuery
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
