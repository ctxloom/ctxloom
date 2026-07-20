package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// A tight 5-tool surface: list, add, set_status, edit, tag. Search rides on
// task_list's term filter; summary on its include_summary flag — saving the
// tool-tax of extra MCP entries.

type taskListInput struct {
	Statuses         []string `json:"statuses,omitempty" jsonschema:"Optional list of statuses to filter by (e.g. [\"In Progress\", \"To Do\"]). Empty = active tasks only (completed Done/Archived and Deferred tasks are hidden unless include_completed is set or such a status is named here)."`
	Term             string   `json:"term,omitempty" jsonschema:"Optional case-insensitive substring filter against task text."`
	TagQuery         string   `json:"tag_query,omitempty" jsonschema:"Optional postfix (RPN) boolean tag filter, e.g. \"urgent/release/and\" (tagged both urgent AND release), \"urgent/release/or\", or \"urgent/not\" (not tagged urgent). A bare slash-separated list with no operator is an implicit AND: \"urgent/release\" behaves like \"urgent/release/and\". Empty = no tag filter."`
	IncludeCompleted bool     `json:"include_completed,omitempty" jsonschema:"When true, include the tasks hidden by default: completed (Done/Archived) and Deferred ones. When a filter matches hidden tasks, the result's hidden_completed/hidden_deferred counts say how many were suppressed."`
	IncludeSummary   bool     `json:"include_summary,omitempty" jsonschema:"When true, include per-status counts and the in-progress harp IDs alongside the task list. Counts always cover every task, including completed ones. Ignored (no summary is returned) when global is set."`
	Global           bool     `json:"global,omitempty" jsonschema:"When true, aggregate tasks across EVERY project instead of just the current one. Off by default: task_list scopes to the project resolved from the working directory. Automatically turned on (with notice set) when no project can be resolved at all."`
}

type taskListResult struct {
	Path       string         `json:"path"`
	ProjectID  string         `json:"project_id,omitempty"`
	ProjectDir string         `json:"project_dir,omitempty"`
	Tasks      []taskRow      `json:"tasks"`
	Summary    *tasks.Summary `json:"summary,omitempty"`

	// Global is true when this listing aggregated every project rather than
	// the one resolved from the working directory — either because the
	// caller asked for it (task_list's global=true) or because no project
	// could be resolved at all (see Notice). ProjectID/ProjectDir are empty
	// in that case; each row in Tasks carries its own project_id instead.
	// ProjectCount is the number of project stores the aggregation scanned.
	Global       bool `json:"global,omitempty"`
	ProjectCount int  `json:"project_count,omitempty"`

	// Notice is set when a listing fell back to Global on its own — no
	// CTXLOOM_PROJECT_ID/--project pin, no CTXLOOM_ROOT or enclosing git repo,
	// and no established identity for the working directory — so the caller
	// sees WHY it got every project's tasks instead of silently getting an
	// unexpected result. Empty otherwise, including for an explicit
	// global=true (an opt-in needs no explanation).
	Notice string `json:"notice,omitempty"`

	// HiddenCompleted/HiddenDeferred count tasks that matched the requested
	// filters but were suppressed by the default active-only view, so a
	// filtered listing never silently truncates its answer. Zero (omitted)
	// when include_completed or an explicit status filter was used.
	HiddenCompleted int `json:"hidden_completed,omitempty"`
	HiddenDeferred  int `json:"hidden_deferred,omitempty"`
}

type taskAddInput struct {
	Text    string   `json:"text" jsonschema:"Task text. Required, trimmed."`
	Status  string   `json:"status,omitempty" jsonschema:"Initial status (default: \"To Do\"). Free-form; standard values are \"In Progress\", \"To Do\", \"Deferred\", \"Done\", \"Archived\"."`
	Trigger string   `json:"trigger,omitempty" jsonschema:"The revive condition for a Deferred task: a concrete description of what should bring it back (e.g. \"the v2 API ships\"). REQUIRED when status is \"Deferred\"; ignored otherwise."`
	Tags    []string `json:"tags,omitempty" jsonschema:"Optional flat tags to set on the task at creation (e.g. [\"urgent\", \"release\"]). Add more later with task_tag."`
}

type taskAddResult struct {
	Path string     `json:"path"`
	Task tasks.Task `json:"task"`
}

type taskSetStatusInput struct {
	HarpID  string `json:"harp_id" jsonschema:"The task's harp ID (e.g. \"swift-amber-falcon\") as returned by task_list or task_add."`
	Status  string `json:"status" jsonschema:"Target status. Standard values: \"In Progress\", \"To Do\", \"Deferred\", \"Done\", \"Archived\"."`
	Trigger string `json:"trigger,omitempty" jsonschema:"The revive condition when moving to \"Deferred\": what should bring the task back. REQUIRED for \"Deferred\" unless the task already carries a trigger (then it is preserved). Ignored for other statuses."`
}

type taskSetStatusResult struct {
	Path string     `json:"path"`
	Task tasks.Task `json:"task"`
}

type taskEditInput struct {
	HarpID string `json:"harp_id" jsonschema:"The task's harp ID (e.g. \"swift-amber-falcon\") as returned by task_list or task_add."`
	Text   string `json:"text" jsonschema:"The full replacement text for the task. The entire text is replaced (not patched); status and trigger are left unchanged."`
}

type taskEditResult struct {
	Path string     `json:"path"`
	Task tasks.Task `json:"task"`
}

type taskTagInput struct {
	HarpID string   `json:"harp_id" jsonschema:"The task's harp ID (e.g. \"swift-amber-falcon\") as returned by task_list or task_add."`
	Add    []string `json:"add,omitempty" jsonschema:"Tags to add (union onto the task's current tag set). Duplicates and already-present tags are no-ops."`
	Remove []string `json:"remove,omitempty" jsonschema:"Tags to remove (subtracted from the task's current tag set). Removing an absent tag is a no-op. At least one of add/remove is required; a tag named in both ends up removed."`
}

type taskTagResult struct {
	Path string     `json:"path"`
	Task tasks.Task `json:"task"`
}

// This and the sibling registerXTools functions elsewhere in the ctxloom
// family (registerAgentTools, registerMemoryTools) share a duplicate shape by
// construction (a run of mcp.AddTool calls). Their tool descriptions are
// independent content; a change to one implies nothing about the others.
// reprise:accept-drift
func registerTaskTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_list",
			Description: "List tasks, optionally filtered by status, text term, or tag query (tag_query). By default this is scoped to the CURRENT project (resolved from the working directory); pass global=true to aggregate every project instead. When no project can be resolved at all (not in a git repo, no CTXLOOM_ROOT, no prior task history there), the listing automatically falls back to global and the result's notice field says so. Completed (Done/Archived) and Deferred tasks are hidden unless include_completed is set; when a filter matches hidden tasks the result reports hidden_completed/hidden_deferred counts. Pass include_summary=true to also get per-status counts and the in-progress harp IDs (single-project only). Echo a task's harp_id back when you reference that task in a later call (e.g. task_set_status).",
		},
		handleTaskList)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_add",
			Description: "Add a new task to the project's task log. Returns the assigned harp ID; reference the task by that ID in subsequent calls.",
		},
		handleTaskAdd)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_set_status",
			Description: "Move a task to a different status. Use \"Done\" to complete a task or \"Archived\" to drop it from the active list without losing history. Use \"Deferred\" with a `trigger` to park a task on a named revive condition (it then hides from the active list until the condition fires).",
		},
		handleTaskSetStatus)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_edit",
			Description: "Replace a task's text in place, keyed by its harp ID. Pass the full new text (the whole text is replaced, not patched); status and any Deferred trigger are left unchanged.",
		},
		handleTaskEdit)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_tag",
			Description: "Add and/or remove flat tags on a task, keyed by its harp ID. Pass `add`, `remove`, or both in one call (add is applied before remove, so a tag in both lists ends up removed). Filter task_list with tag_query using the resulting tags.",
		},
		handleTaskTag)
}

func handleTaskList(_ context.Context, _ *mcp.CallToolRequest, in taskListInput) (*mcp.CallToolResult, *taskListResult, error) {
	tc, err := taskContext()
	if err != nil {
		return nil, nil, err
	}
	scope, err := resolveListScope(in.Global, tc.ProjectID, tc.WorkDir)
	if err != nil {
		return nil, nil, err
	}

	if scope.Global {
		gres, err := listAllProjects(in.Statuses, in.Term, in.TagQuery, in.IncludeCompleted, tc.SessionHarp)
		if err != nil {
			return nil, nil, err
		}
		return nil, &taskListResult{
			Tasks:           gres.Rows,
			Global:          true,
			ProjectCount:    gres.ProjectCount,
			Notice:          scope.Notice,
			HiddenCompleted: gres.HiddenCompleted,
			HiddenDeferred:  gres.HiddenDeferred,
		}, nil
	}

	tc, err = requireHoming(tc)
	if err != nil {
		return nil, nil, err
	}
	res, err := operations.ListTasksWithTagQuery(tc, in.Statuses, in.Term, in.TagQuery, in.IncludeCompleted, in.IncludeSummary)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	rows := make([]taskRow, len(res.Tasks))
	for i, t := range res.Tasks {
		rows[i] = taskRow{Task: t, ProjectID: res.ProjectID, ProjectDir: res.ProjectDir}
	}
	out := &taskListResult{
		Path:            res.Path,
		ProjectID:       res.ProjectID,
		ProjectDir:      res.ProjectDir,
		Tasks:           rows,
		Summary:         res.Summary,
		HiddenCompleted: res.HiddenCompleted,
		HiddenDeferred:  res.HiddenDeferred,
	}
	return nil, out, nil
}

func handleTaskAdd(_ context.Context, _ *mcp.CallToolRequest, in taskAddInput) (*mcp.CallToolResult, *taskAddResult, error) {
	tc, err := taskContextSingle()
	if err != nil {
		return nil, nil, err
	}
	res, err := operations.AddTaskWithTags(tc, in.Text, in.Status, in.Trigger, in.Tags)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskAddResult{Path: res.Path, Task: res.Task}, nil
}

func handleTaskSetStatus(_ context.Context, _ *mcp.CallToolRequest, in taskSetStatusInput) (*mcp.CallToolResult, *taskSetStatusResult, error) {
	tc, err := taskContextSingle()
	if err != nil {
		return nil, nil, err
	}
	res, err := operations.SetTaskStatus(tc, in.HarpID, in.Status, in.Trigger)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskSetStatusResult{Path: res.Path, Task: res.Task}, nil
}

func handleTaskEdit(_ context.Context, _ *mcp.CallToolRequest, in taskEditInput) (*mcp.CallToolResult, *taskEditResult, error) {
	tc, err := taskContextSingle()
	if err != nil {
		return nil, nil, err
	}
	res, err := operations.EditTask(tc, in.HarpID, in.Text)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskEditResult{Path: res.Path, Task: res.Task}, nil
}

func handleTaskTag(_ context.Context, _ *mcp.CallToolRequest, in taskTagInput) (*mcp.CallToolResult, *taskTagResult, error) {
	tc, err := taskContextSingle()
	if err != nil {
		return nil, nil, err
	}
	res, err := operations.TagTask(tc, in.HarpID, in.Add, in.Remove)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskTagResult{Path: res.Path, Task: res.Task}, nil
}
