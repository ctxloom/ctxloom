package tasks

import "fmt"

// Store is the public face of a per-project task log. The append-only JSONL
// log is the only backend: the pre-ADR-0025 markdown store and its one-time
// migration were dropped at extraction (re-adding a task is the upgrade path
// for anything that old).
//
// The store is driven concurrently by separate processes (the MCP server and
// the bare `tasks` CLI) as well as goroutines within one process. Mutations
// hold both an in-process mutex and a cross-process advisory file lock
// spanning the whole fold-and-append.
type Store struct {
	log *eventLog
}

// OpenLog returns a Store backed by the append-only per-project task log at
// path (see paths.TasksLogPath). sessionHarp is stamped as the origin on
// created tasks and the actor on status/remove events; it may be empty (e.g. a
// bare CLI invocation outside a session).
func OpenLog(path, sessionHarp string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("log path required")
	}
	return &Store{log: &eventLog{path: path, session: sessionHarp}}, nil
}

// Path returns the absolute path of the task log file.
func (s *Store) Path() string { return s.log.path }

// List returns tasks filtered by status (empty = all statuses) and term
// (empty = no filter). Term is matched case-insensitively as a substring
// of the trimmed task text.
func (s *Store) List(statuses []string, term string) ([]Task, error) {
	return s.log.list(statuses, term)
}

// Add appends a new task with an auto-generated unique harp ID. Empty
// status defaults to StatusToDo. Returns the persisted task.
func (s *Store) Add(text, status string) (Task, error) {
	return s.AddWithTrigger(text, status, "")
}

// AddWithTrigger is Add with a revive trigger for a Deferred task. A Deferred
// status without a trigger is rejected (ErrTriggerRequired).
func (s *Store) AddWithTrigger(text, status, trigger string) (Task, error) {
	if err := ValidateStatusTrigger(status, trigger); err != nil {
		return Task{}, err
	}
	return s.log.add(text, status, trigger)
}

// Remove tombstones the task with harpID and returns the removed task. Errors
// if the harp ID isn't present. The harp is never reissued.
func (s *Store) Remove(harpID string) (Task, error) {
	return s.log.remove(harpID)
}

// SetStatus moves a task to a different status. Errors if the harp ID
// isn't present.
func (s *Store) SetStatus(harpID, status string) (Task, error) {
	return s.SetStatusWithTrigger(harpID, status, "")
}

// SetStatusWithTrigger moves a task to a different status, optionally setting
// its revive trigger. Moving to Deferred requires a trigger: the one supplied
// here, or the task's existing trigger if it already had one. An empty trigger
// argument never clears an existing one, so a task can cycle Deferred → To Do →
// Deferred without re-typing the condition.
func (s *Store) SetStatusWithTrigger(harpID, status, trigger string) (Task, error) {
	return s.log.setStatus(harpID, status, trigger)
}

// SetText replaces a task's text in place, keyed by harp ID. The whole text is
// replaced (not patched); status and trigger are untouched. Errors if the harp
// ID isn't present or the new text is empty.
func (s *Store) SetText(harpID, text string) (Task, error) {
	return s.log.setText(harpID, text)
}

// Snapshot returns every task in the store, in add order.
func (s *Store) Snapshot() ([]Task, error) {
	return s.log.snapshot()
}

// Repair re-introduces any displaced duplicate-add the log detected, under a
// fresh harp (idempotent). A no-op on a clean log.
func (s *Store) Repair() error {
	return s.log.repair()
}

// Summarize counts tasks per status. Deterministic; no LLM call.
func (s *Store) Summarize() (Summary, error) {
	return s.log.summarize()
}
