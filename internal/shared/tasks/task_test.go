package tasks

import (
	"encoding/json"
	"testing"
)

// Statuses is the taxonomy a client renders instead of hardcoding the status
// set: it must stay in display order and mark the terminal (Done/Archived) and
// trigger-requiring (Deferred) statuses correctly.
func TestStatuses(t *testing.T) {
	got := Statuses()
	if len(got) != len(DefaultStatusOrder) {
		t.Fatalf("Statuses() returned %d entries, want %d", len(got), len(DefaultStatusOrder))
	}
	for i, s := range got {
		if s.Name != DefaultStatusOrder[i] {
			t.Errorf("entry %d: name %q, want %q", i, s.Name, DefaultStatusOrder[i])
		}
		if s.Order != i {
			t.Errorf("entry %d: order %d, want %d", i, s.Order, i)
		}
		wantTerminal := s.Name == StatusDone || s.Name == StatusArchived
		if s.Terminal != wantTerminal {
			t.Errorf("%s: terminal %v, want %v", s.Name, s.Terminal, wantTerminal)
		}
		wantTrigger := s.Name == StatusDeferred
		if s.RequiresTrigger != wantTrigger {
			t.Errorf("%s: requires_trigger %v, want %v", s.Name, s.RequiresTrigger, wantTrigger)
		}
	}
}

// Task's JSON shape is a cross-surface contract: `taskloom list --json`
// marshals it directly, and the taskloom MCP tools emit the same snake_case
// keys (harp_id, text, status, ...). A scripted consumer must be able to
// treat both surfaces identically.
func TestTaskMarshalsSnakeCase(t *testing.T) {
	b, err := json.Marshal(Task{
		HarpID:        "swift-amber-falcon",
		Text:          "do the thing",
		Status:        StatusToDo,
		TextHash:      "abc123def456",
		Trigger:       "v2 ships",
		OriginSession: "zesty-slack-wager",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"harp_id", "text", "status", "checked", "text_hash", "trigger", "origin_session"} {
		if _, ok := got[key]; !ok {
			t.Errorf("marshaled Task missing %q; keys: %v", key, got)
		}
	}
}

func TestTaskMarshalOmitsEmptyOptionalFields(t *testing.T) {
	b, err := json.Marshal(Task{HarpID: "old-dill", Text: "x", Status: StatusToDo})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"trigger", "origin_session"} {
		if _, ok := got[key]; ok {
			t.Errorf("empty %q should be omitted; keys: %v", key, got)
		}
	}
}

func TestSummaryMarshalsSnakeCase(t *testing.T) {
	b, err := json.Marshal(Summary{Counts: map[string]int{StatusToDo: 1}, InProgress: []string{"old-dill"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["in_progress"]; !ok {
		t.Errorf("marshaled Summary missing in_progress; keys: %v", got)
	}
}
