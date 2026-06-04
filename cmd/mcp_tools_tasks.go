package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/tasks"
)

// Task tools follow Phase 4.4 of docs/ctxloom-tasks-plan.md: a tight
// 3-tool surface (list, add, set_status). task_search is absorbed into
// task_list's term filter; task_summary is exposed via list's
// `include_summary` flag — saving the tool-tax of two extra MCP entries.

type taskListInput struct {
	Statuses         []string `json:"statuses,omitempty" jsonschema:"Optional list of statuses to filter by (e.g. [\"In Progress\", \"To Do\"]). Empty = active tasks only (completed tasks are hidden unless include_completed is set or a completed status is named here)."`
	Term             string   `json:"term,omitempty" jsonschema:"Optional case-insensitive substring filter against task text."`
	IncludeCompleted bool     `json:"include_completed,omitempty" jsonschema:"When true, include completed (Done/Archived) tasks, which are hidden by default."`
	IncludeSummary   bool     `json:"include_summary,omitempty" jsonschema:"When true, include per-status counts and the in-progress harp IDs alongside the task list. Counts always cover every task, including completed ones."`
}

type taskListResult struct {
	Path    string      `json:"path"`
	Tasks   []taskOut   `json:"tasks"`
	Summary *summaryOut `json:"summary,omitempty"`
}

type taskOut struct {
	HarpID  string `json:"harp_id"`
	Text    string `json:"text"`
	Status  string `json:"status"`
	Checked bool   `json:"checked"`
	Trigger string `json:"trigger,omitempty"`
}

type summaryOut struct {
	Counts     map[string]int `json:"counts"`
	InProgress []string       `json:"in_progress"`
}

type taskAddInput struct {
	Text    string `json:"text" jsonschema:"Task text. Required, trimmed."`
	Status  string `json:"status,omitempty" jsonschema:"Initial status (default: \"To Do\"). Free-form; standard values are \"In Progress\", \"To Do\", \"Deferred\", \"Done\", \"Archived\"."`
	Trigger string `json:"trigger,omitempty" jsonschema:"The revive condition for a Deferred task: a concrete description of what should bring it back (e.g. \"the v2 API ships\"). REQUIRED when status is \"Deferred\"; ignored otherwise."`
}

type taskAddResult struct {
	Path string  `json:"path"`
	Task taskOut `json:"task"`
}

type taskSetStatusInput struct {
	HarpID  string `json:"harp_id" jsonschema:"The task's harp ID (e.g. \"swift-amber-falcon\") as returned by task_list or task_add."`
	Status  string `json:"status" jsonschema:"Target status. Standard values: \"In Progress\", \"To Do\", \"Deferred\", \"Done\", \"Archived\"."`
	Trigger string `json:"trigger,omitempty" jsonschema:"The revive condition when moving to \"Deferred\": what should bring the task back. REQUIRED for \"Deferred\" unless the task already carries a trigger (then it is preserved). Ignored for other statuses."`
}

type taskSetStatusResult struct {
	Path string  `json:"path"`
	Task taskOut `json:"task"`
}

type taskEditInput struct {
	HarpID string `json:"harp_id" jsonschema:"The task's harp ID (e.g. \"swift-amber-falcon\") as returned by task_list or task_add."`
	Text   string `json:"text" jsonschema:"The full replacement text for the task. The entire text is replaced (not patched); status and trigger are left unchanged."`
}

type taskEditResult struct {
	Path string  `json:"path"`
	Task taskOut `json:"task"`
}

func (s *ctxServer) registerTaskTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_list",
			Description: "List tasks from .ctxloom/tasks.md, optionally filtered by status or text term. Pass include_summary=true to also get per-status counts and the in-progress harp IDs. Echo a task's harp_id back when you reference that task in a later call (e.g. task_set_status).",
		},
		s.handleTaskList)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_add",
			Description: "Add a new task to .ctxloom/tasks.md. Returns the assigned harp ID; reference the task by that ID in subsequent calls.",
		},
		s.handleTaskAdd)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_set_status",
			Description: "Move a task to a different status section. Use \"Done\" to complete a task or \"Archived\" to drop it from the active list without losing history. Use \"Deferred\" with a `trigger` to park a task on a named revive condition (it then hides from the active list until the condition fires — see the check-triggers skill).",
		},
		s.handleTaskSetStatus)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "task_edit",
			Description: "Replace a task's text in place, keyed by its harp ID. Pass the full new text (the whole text is replaced, not patched); status and any Deferred trigger are left unchanged.",
		},
		s.handleTaskEdit)
}

func (s *ctxServer) handleTaskList(_ context.Context, _ *mcp.CallToolRequest, in taskListInput) (*mcp.CallToolResult, *taskListResult, error) {
	res, err := operations.ListTasks(taskContext(), in.Statuses, in.Term, in.IncludeCompleted, in.IncludeSummary)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	out := &taskListResult{
		Path:  res.Path,
		Tasks: toTaskOuts(res.Tasks),
	}
	if res.Summary != nil {
		out.Summary = &summaryOut{Counts: res.Summary.Counts, InProgress: res.Summary.InProgress}
	}
	return nil, out, nil
}

func (s *ctxServer) handleTaskAdd(_ context.Context, _ *mcp.CallToolRequest, in taskAddInput) (*mcp.CallToolResult, *taskAddResult, error) {
	res, err := operations.AddTask(taskContext(), in.Text, in.Status, in.Trigger)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskAddResult{Path: res.Path, Task: toTaskOut(res.Task)}, nil
}

func (s *ctxServer) handleTaskSetStatus(_ context.Context, _ *mcp.CallToolRequest, in taskSetStatusInput) (*mcp.CallToolResult, *taskSetStatusResult, error) {
	res, err := operations.SetTaskStatus(taskContext(), in.HarpID, in.Status, in.Trigger)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskSetStatusResult{Path: res.Path, Task: toTaskOut(res.Task)}, nil
}

func (s *ctxServer) handleTaskEdit(_ context.Context, _ *mcp.CallToolRequest, in taskEditInput) (*mcp.CallToolResult, *taskEditResult, error) {
	res, err := operations.EditTask(taskContext(), in.HarpID, in.Text)
	if err != nil {
		return nil, nil, err
	}
	warnTask(res.Warning)
	return nil, &taskEditResult{Path: res.Path, Task: toTaskOut(res.Task)}, nil
}

func toTaskOut(t tasks.Task) taskOut {
	return taskOut{HarpID: t.HarpID, Text: t.Text, Status: t.Status, Checked: t.Checked, Trigger: t.Trigger}
}

func toTaskOuts(list []tasks.Task) []taskOut {
	out := make([]taskOut, len(list))
	for i, t := range list {
		out[i] = toTaskOut(t)
	}
	return out
}
