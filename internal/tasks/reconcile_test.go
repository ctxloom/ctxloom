package tasks

import (
	"testing"
)

func TestStripHarpID(t *testing.T) {
	tests := []struct {
		in       string
		wantID   string
		wantText string
	}{
		{"`swift-amber-falcon` do the thing", "swift-amber-falcon", "do the thing"},
		{"  `bold-otter`  with leading whitespace", "bold-otter", "with leading whitespace"},
		{"no harp id here", "", "no harp id here"},
		{"`malformed text without space", "", "`malformed text without space"},
		{"", "", ""},
	}
	for _, tc := range tests {
		id, text := stripHarpID(tc.in)
		if id != tc.wantID || text != tc.wantText {
			t.Errorf("stripHarpID(%q) = (%q, %q); want (%q, %q)", tc.in, id, text, tc.wantID, tc.wantText)
		}
	}
}

func TestMapTodoStatus(t *testing.T) {
	cases := map[string]string{
		"pending":     StatusToDo,
		"to do":       StatusToDo,
		"TODO":        StatusToDo,
		"":            StatusToDo,
		"in_progress": StatusInProgress,
		"In Progress": StatusInProgress,
		"completed":   StatusDone,
		"done":        StatusDone,
		"cancelled":   StatusArchived,
		"archived":    StatusArchived,
		"wat":         StatusToDo,
	}
	for in, want := range cases {
		if got := MapTodoStatus(in); got != want {
			t.Errorf("MapTodoStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReconcile_NewItemsGetHarpIDs(t *testing.T) {
	s, _ := newTestStore(t)
	diff, err := s.Reconcile([]SnapshotItem{
		{Text: "first task", Status: StatusToDo},
		{Text: "second task", Status: StatusInProgress},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(diff.Added) != 2 {
		t.Errorf("Added: want 2, got %d", len(diff.Added))
	}
	for _, task := range diff.Added {
		if task.HarpID == "" {
			t.Error("Added task should have a harp ID")
		}
	}

	all, _ := s.Snapshot()
	if len(all) != 2 {
		t.Errorf("store should hold 2 tasks, got %d", len(all))
	}
}

func TestReconcile_HarpIDMatchUpdates(t *testing.T) {
	s, _ := newTestStore(t)
	// First reconcile creates the task.
	diff, _ := s.Reconcile([]SnapshotItem{
		{Text: "draft proposal", Status: StatusToDo},
	})
	if len(diff.Added) != 1 {
		t.Fatalf("setup: expected 1 Added, got %d", len(diff.Added))
	}
	harpID := diff.Added[0].HarpID

	// LLM round-trips the harp ID and bumps status to in_progress.
	diff, err := s.Reconcile([]SnapshotItem{
		{Text: "`" + harpID + "` draft proposal", Status: StatusInProgress},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(diff.Added) != 0 {
		t.Errorf("nothing new should be added; got %d", len(diff.Added))
	}
	if len(diff.Updated) != 1 {
		t.Fatalf("status change should produce 1 Updated; got %d", len(diff.Updated))
	}
	if diff.Updated[0].HarpID != harpID || diff.Updated[0].Status != StatusInProgress {
		t.Errorf("Updated: got %+v", diff.Updated[0])
	}
}

func TestReconcile_TextHashFallback(t *testing.T) {
	s, _ := newTestStore(t)
	// Seed via Add (so we know the harp ID); same text without the prefix.
	seeded, _ := s.Add("port harp to Go", StatusToDo)

	// LLM forgot to echo the harp ID, but text matches exactly.
	diff, err := s.Reconcile([]SnapshotItem{
		{Text: "port harp to Go", Status: StatusInProgress},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(diff.Added) != 0 {
		t.Errorf("hash should match existing; nothing should be Added; got %d", len(diff.Added))
	}
	if len(diff.Updated) != 1 || diff.Updated[0].HarpID != seeded.HarpID {
		t.Fatalf("expected Updated for %s; got %+v", seeded.HarpID, diff.Updated)
	}
}

func TestReconcile_ArchivesMissingItems(t *testing.T) {
	s, _ := newTestStore(t)
	keep, _ := s.Add("keep me", StatusToDo)
	drop, _ := s.Add("drop me", StatusInProgress)

	// Snapshot only mentions `keep`.
	_, err := s.Reconcile([]SnapshotItem{
		{Text: "`" + keep.HarpID + "` keep me", Status: StatusToDo},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	all, _ := s.Snapshot()
	for _, task := range all {
		if task.HarpID == drop.HarpID {
			if task.Status != StatusArchived {
				t.Errorf("disappeared task %s should be Archived, got %q", drop.HarpID, task.Status)
			}
			return
		}
	}
	t.Errorf("dropped task %s should still exist (archived), missing entirely", drop.HarpID)
}

func TestReconcile_AlreadyArchivedNotTouched(t *testing.T) {
	s, _ := newTestStore(t)
	old, _ := s.Add("ancient task", StatusArchived)

	_, _ = s.Reconcile([]SnapshotItem{
		{Text: "something new", Status: StatusToDo},
	})

	all, _ := s.Snapshot()
	for _, task := range all {
		if task.HarpID == old.HarpID {
			if task.Status != StatusArchived {
				t.Errorf("pre-archived task should stay Archived, got %q", task.Status)
			}
			return
		}
	}
	t.Errorf("archived task %s vanished", old.HarpID)
}

func TestReconcile_TextEditPreservesIdentity(t *testing.T) {
	s, _ := newTestStore(t)
	original, _ := s.Add("write the doc", StatusInProgress)

	// LLM rewords but echoes the harp ID.
	diff, _ := s.Reconcile([]SnapshotItem{
		{Text: "`" + original.HarpID + "` write the design doc", Status: StatusInProgress},
	})
	if len(diff.Updated) != 1 {
		t.Fatalf("text edit should produce 1 Updated; got %d", len(diff.Updated))
	}
	if diff.Updated[0].Text != "write the design doc" {
		t.Errorf("text not updated: %q", diff.Updated[0].Text)
	}
	if diff.Updated[0].HarpID != original.HarpID {
		t.Errorf("harp ID changed across edit: %q -> %q", original.HarpID, diff.Updated[0].HarpID)
	}
}
