package tasks

import (
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

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

// ListWithTagQuery is List with an additional postfix tag-query filter,
// evaluated by tagma (see filterTasks, e.g. "urgent/release/and"). An empty
// tagQuery behaves exactly like List. A malformed tagQuery is returned as an
// error rather than a silently empty or unfiltered result. schema — the
// project's resolved tag-schema, or nil — feeds the tag-query index's
// client-loadable type comparison (tagma SPEC.md §9), e.g. so a
// `tagma.type:"triage:blocks-release"=semver` declaration makes a relational
// query ('<=' etc.) over that target compare by real SemVer 2.0.0 precedence
// instead of tagma's own numeric grammar. nil behaves exactly as before this
// feature existed.
func (s *Store) ListWithTagQuery(statuses []string, term, tagQuery string, schema *tagschema.Schema) ([]Task, error) {
	return s.log.listWithTagQuery(statuses, term, tagQuery, schema)
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

// AddWithTags is AddWithTrigger with an initial tag set stamped on the same
// `add` event, so a task's creation and its starting tags land as one atomic
// log line rather than an add followed by a separate tag event.
func (s *Store) AddWithTags(text, status, trigger string, tags ...string) (Task, error) {
	if err := ValidateStatusTrigger(status, trigger); err != nil {
		return Task{}, err
	}
	return s.log.addWithTags(text, status, trigger, tags)
}

// AddTags unions tags onto an existing task's tag set (append opTag),
// attributing the change to the store's session. Duplicate or
// already-present tags are no-ops; at least one tag is required. Errors if
// the harp ID isn't present.
func (s *Store) AddTags(harpID string, tags ...string) (Task, error) {
	return s.log.addTags(harpID, tags)
}

// RemoveTags subtracts tags from an existing task's tag set (append
// opUntag), attributing the change to the store's session. Removing an
// absent tag is a no-op for that tag; at least one tag is required. Errors
// if the harp ID isn't present.
func (s *Store) RemoveTags(harpID string, tags ...string) (Task, error) {
	return s.log.removeTags(harpID, tags)
}

// CurrentTags returns harpID's current (folded) tag set, without mutating
// anything. Used by the write-seam scalar-collapse in
// internal/shared/tasks/operations to compare an incoming tag against what
// the task already carries before deciding whether a collapsing untag is
// needed. Errors if the harp ID isn't present.
func (s *Store) CurrentTags(harpID string) ([]string, error) {
	return s.log.currentTags(harpID)
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

// DeferredSince returns, for every currently Deferred task, the timestamp of
// the event that most recently moved it into Deferred status (an add with
// status Deferred, or a later status change to Deferred). Tasks that are not
// currently Deferred are omitted, even if they were deferred in the past.
// Trigger evaluation uses this to scope its evidence gathering (e.g. git
// history) to what happened since a task was parked, without walking the
// whole project history.
func (s *Store) DeferredSince() (map[string]time.Time, error) {
	return s.log.deferredSince()
}
