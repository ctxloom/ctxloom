package tasks

import (
	"encoding/json"
	"testing"
)

func taskWithTags(harpID string, tags ...string) Task {
	return Task{HarpID: harpID, Text: harpID, Status: StatusToDo, Tags: tags}
}

// filterTasks is the tag-query engine every list surface (Store.List,
// operations.ListTasks*, the CLI, and MCP task_list) ultimately runs
// through. These cases pin the postfix grammar wiring (and, or, not,
// implicit-AND) plus the fail-loud contract on a malformed query.
func TestFilterTasksTagQuery(t *testing.T) {
	all := []Task{
		taskWithTags("urgent-release", "urgent", "release"),
		taskWithTags("urgent-only", "urgent"),
		taskWithTags("release-only", "release"),
		taskWithTags("untagged"),
	}

	cases := []struct {
		name    string
		query   string
		wantIDs []string
	}{
		{"and", "urgent/release/and", []string{"urgent-release"}},
		{"or", "urgent/release/or", []string{"urgent-release", "urgent-only", "release-only"}},
		{"not", "urgent/not", []string{"release-only", "untagged"}},
		{"implicit and", "urgent/release", []string{"urgent-release"}},
		{"empty is no filter", "", []string{"urgent-release", "urgent-only", "release-only", "untagged"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := filterTasks(all, nil, "", c.query)
			if err != nil {
				t.Fatalf("filterTasks(%q): %v", c.query, err)
			}
			var ids []string
			for _, task := range got {
				ids = append(ids, task.HarpID)
			}
			if len(ids) != len(c.wantIDs) {
				t.Fatalf("query %q: got %v, want %v", c.query, ids, c.wantIDs)
			}
			want := map[string]bool{}
			for _, id := range c.wantIDs {
				want[id] = true
			}
			for _, id := range ids {
				if !want[id] {
					t.Errorf("query %q: unexpected id %q in %v", c.query, id, ids)
				}
			}
		})
	}
}

// A malformed tag query must fail loud (a user-facing error), never degrade
// to a silent empty or unfiltered result.
func TestFilterTasksMalformedTagQueryErrors(t *testing.T) {
	all := []Task{taskWithTags("a", "urgent")}
	_, err := filterTasks(all, nil, "", "and")
	if err == nil {
		t.Fatal("expected an error for a malformed tag query (bare operator, arity underflow)")
	}
}

func TestNormalizeTags(t *testing.T) {
	got := normalizeTags([]string{"beta", " alpha ", "beta", "", "  "})
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("normalizeTags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeTags = %v, want %v", got, want)
		}
	}
	if normalizeTags(nil) != nil {
		t.Fatalf("normalizeTags(nil) should stay nil, got %v", normalizeTags(nil))
	}
}

func TestUnionAndSubtractTags(t *testing.T) {
	base := normalizeTags([]string{"alpha", "beta"})

	union := unionTags(base, []string{"beta", "gamma"})
	if got := union; len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Fatalf("unionTags = %v, want [alpha beta gamma]", got)
	}
	// Idempotent: unioning an already-present tag changes nothing.
	again := unionTags(union, []string{"beta"})
	if len(again) != 3 {
		t.Fatalf("unionTags idempotence: got %v", again)
	}

	sub := subtractTags(union, []string{"beta"})
	if len(sub) != 2 || sub[0] != "alpha" || sub[1] != "gamma" {
		t.Fatalf("subtractTags = %v, want [alpha gamma]", sub)
	}
	// Removing an absent tag is a no-op.
	noop := subtractTags(sub, []string{"nonexistent"})
	if len(noop) != 2 || noop[0] != "alpha" || noop[1] != "gamma" {
		t.Fatalf("subtractTags no-op = %v, want [alpha gamma]", noop)
	}
}

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
		Tags:          []string{"release", "urgent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"harp_id", "text", "status", "checked", "text_hash", "trigger", "origin_session", "tags"} {
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
	for _, key := range []string{"trigger", "origin_session", "tags"} {
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
