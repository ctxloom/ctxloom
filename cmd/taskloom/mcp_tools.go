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
	IncludeSummary   bool     `json:"include_summary,omitempty" jsonschema:"When true, include per-status counts and the in-progress harp IDs alongside the task list. Counts always cover every task, including completed ones."`
}

type taskListResult struct {
	Path       string         `json:"path"`
	ProjectID  string         `json:"project_id"`
	ProjectDir string         `json:"project_dir,omitempty"`
	Tasks      []tasks.Task   `json:"tasks"`
	Summary    *tasks.Summary `json:"summary,omitempty"`

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

func registerTaskTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_list",
			Description: "List the project's tasks, optionally filtered by status, text term, or tag query (tag_query). Completed (Done/Archived) and Deferred tasks are hidden unless include_completed is set; when a filter matches hidden tasks the result reports hidden_completed/hidden_deferred counts. Pass include_summary=true to also get per-status counts and the in-progress harp IDs. Echo a task's harp_id back when you reference that task in a later call (e.g. task_set_status).",
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
	res, err := operations.ListTasksWithTagQuery(taskContext(), in.Statuses, in.Term, in.TagQuery, in.IncludeCompleted, in.IncludeSummary)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	out := &taskListResult{
		Path:            res.Path,
		ProjectID:       res.ProjectID,
		ProjectDir:      res.ProjectDir,
		Tasks:           res.Tasks,
		Summary:         res.Summary,
		HiddenCompleted: res.HiddenCompleted,
		HiddenDeferred:  res.HiddenDeferred,
	}
	return nil, out, nil
}

func handleTaskAdd(_ context.Context, _ *mcp.CallToolRequest, in taskAddInput) (*mcp.CallToolResult, *taskAddResult, error) {
	res, err := operations.AddTaskWithTags(taskContext(), in.Text, in.Status, in.Trigger, in.Tags)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskAddResult{Path: res.Path, Task: res.Task}, nil
}

func handleTaskSetStatus(_ context.Context, _ *mcp.CallToolRequest, in taskSetStatusInput) (*mcp.CallToolResult, *taskSetStatusResult, error) {
	res, err := operations.SetTaskStatus(taskContext(), in.HarpID, in.Status, in.Trigger)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskSetStatusResult{Path: res.Path, Task: res.Task}, nil
}

func handleTaskEdit(_ context.Context, _ *mcp.CallToolRequest, in taskEditInput) (*mcp.CallToolResult, *taskEditResult, error) {
	res, err := operations.EditTask(taskContext(), in.HarpID, in.Text)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskEditResult{Path: res.Path, Task: res.Task}, nil
}

func handleTaskTag(_ context.Context, _ *mcp.CallToolRequest, in taskTagInput) (*mcp.CallToolResult, *taskTagResult, error) {
	res, err := operations.TagTask(taskContext(), in.HarpID, in.Add, in.Remove)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskTagResult{Path: res.Path, Task: res.Task}, nil
}
