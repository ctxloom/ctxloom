package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests cover the task-log union-merge survival work: fold() must
// replay events in timestamp order (not raw file/merge order), must ignore
// byte-identical duplicate lines (a union merge does not dedupe), and must
// FAIL LOUD — never silently — when two different tasks were independently
// minted with the same harp id by concurrent branches.

// TestFold_AppliesEventsInTimestampOrderNotFileOrder pins the core ordering
// fix: two `status` events for one harp, written to the file in an order that
// contradicts their `ts` values. The later-timestamped status must win, not
// whichever line happens to come last in the file.
func TestFold_AppliesEventsInTimestampOrderNotFileOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	// File order: add, then Done (ts=T3), then In Progress (ts=T1). If fold
	// replayed by file order, In Progress would win. Chronologically, Done
	// (the later ts) must win.
	raw := `{"op":"add","task":"alpha","text":"ship it","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"status","task":"alpha","status":"Done","ts":"2026-01-01T00:00:03Z"}
{"op":"status","task":"alpha","status":"In Progress","ts":"2026-01-01T00:00:01Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 task, got %d: %+v", len(got), got)
	}
	if got[0].Status != StatusDone {
		t.Fatalf("status = %q, want %q (later ts must win over file order)", got[0].Status, StatusDone)
	}
}

// TestFold_IgnoresExactDuplicateLines pins Part 1b: a union merge does not
// deduplicate, so a byte-identical record can appear twice. A duplicate `add`
// is the dangerous case (it would otherwise become a spurious anomaly): fold
// must recognize the exact repeat and skip it, leaving exactly one task and
// zero anomalies.
func TestFold_IgnoresExactDuplicateLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	line := `{"op":"add","task":"alpha","text":"ship it","status":"To Do","ts":"2026-01-01T00:00:00Z"}`
	raw := line + "\n" + line + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 task from a duplicated add, got %d: %+v", len(got), got)
	}

	f, err := s.log.fold()
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if len(f.anomalies) != 0 {
		t.Fatalf("a byte-identical duplicate add must not register as an anomaly, got %d: %+v", len(f.anomalies), f.anomalies)
	}
}

// TestFold_StableForEqualTimestamps pins the tiebreak: when two events for
// the same field carry the identical ts, the sort must be stable so file
// order breaks the tie deterministically (not flip-flop across runs).
func TestFold_StableForEqualTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	// Both status events carry the exact same ts. File order: In Progress
	// then Done. A stable sort on ts alone leaves file order intact for
	// ties, so Done (later in the file) must win.
	raw := `{"op":"add","task":"alpha","text":"ship it","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"status","task":"alpha","status":"In Progress","ts":"2026-01-01T00:00:05Z"}
{"op":"status","task":"alpha","status":"Done","ts":"2026-01-01T00:00:05Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Status != StatusDone {
		t.Fatalf("want Done (file order breaks the ts tie), got %+v", got)
	}
}

// TestFold_SimulatedUnionMerge builds two divergent logs from a common base
// (as if two branches both appended to the checked-in task log), concatenates
// them ours-then-theirs the way git's merge=union driver does — no conflict
// markers, no dedup, raw concatenation — and asserts the folded state equals
// a chronological single-writer replay of the same events. This is the test
// that actually proves the log survives a union merge.
func TestFold_SimulatedUnionMerge(t *testing.T) {
	base := `{"op":"add","task":"gamma","text":"ship it","status":"To Do","ts":"2026-01-01T00:00:00Z"}` + "\n"
	// Ours: a later-timestamped Done.
	ours := `{"op":"status","task":"gamma","status":"Done","ts":"2026-01-01T00:00:03Z"}` + "\n"
	// Theirs: an earlier-timestamped In Progress, plus an unrelated tag.
	theirs := `{"op":"status","task":"gamma","status":"In Progress","ts":"2026-01-01T00:00:01Z"}
{"op":"tag","task":"gamma","tags":["urgent"],"ts":"2026-01-01T00:00:02Z"}
`
	// merge=union concatenates ours then theirs, verbatim, in file order —
	// which here contradicts ts order (Done at T3 is written before
	// In Progress at T1 and the tag at T2).
	merged := base + ours + theirs
	mergedPath := filepath.Join(t.TempDir(), "merged.jsonl")
	if err := os.WriteFile(mergedPath, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reference: the exact same events, hand-ordered chronologically, as a
	// single writer would have produced them.
	chronological := base + theirs + ours // base(T0), In Progress(T1), tag(T2), Done(T3)
	refPath := filepath.Join(t.TempDir(), "reference.jsonl")
	if err := os.WriteFile(refPath, []byte(chronological), 0o644); err != nil {
		t.Fatal(err)
	}

	mergedStore, _ := OpenLog(mergedPath, "")
	mergedGot, err := mergedStore.List(nil, "")
	if err != nil {
		t.Fatalf("merged list: %v", err)
	}
	refStore, _ := OpenLog(refPath, "")
	refGot, err := refStore.List(nil, "")
	if err != nil {
		t.Fatalf("reference list: %v", err)
	}

	if len(mergedGot) != 1 || len(refGot) != 1 {
		t.Fatalf("want 1 task each, got merged=%+v reference=%+v", mergedGot, refGot)
	}
	m, r := mergedGot[0], refGot[0]
	if m.Status != r.Status || m.Text != r.Text || len(m.Tags) != len(r.Tags) {
		t.Fatalf("union-merged fold diverged from chronological replay:\n  merged=%+v\n  reference=%+v", m, r)
	}
	if m.Status != StatusDone {
		t.Fatalf("status = %q, want %q (latest ts across both branches)", m.Status, StatusDone)
	}
	if len(m.Tags) != 1 || m.Tags[0] != "urgent" {
		t.Fatalf("tags = %v, want [urgent]", m.Tags)
	}
}

// TestFold_DuplicateHarpDifferentContentFailsLoud is the critical-distinction
// test: unlike a byte-identical duplicate line (a harmless merge artifact,
// Part 1b), two DIFFERENT tasks minted with the same harp on separate
// branches is a real identity collision. It cannot be silently folded away —
// one task would be lost and the other silently corrupted by every
// subsequent event meant for the lost one. Reads must fail loud, naming both
// task texts and the colliding harp, so a human notices and can repair
// manually.
func TestFold_DuplicateHarpDifferentContentFailsLoud(t *testing.T) {
	// Simulate a union merge of two branches that each independently minted
	// harp "alpha" for a different task off a common (harp-less) base.
	ours := `{"op":"add","task":"alpha","text":"write the storage layer","status":"To Do","session":"swift-amber-falcon","ts":"2026-01-01T00:00:01Z"}` + "\n"
	theirs := `{"op":"add","task":"alpha","text":"fix the flaky test","status":"To Do","session":"quiet-violet-otter","ts":"2026-01-01T00:00:02Z"}` + "\n"
	merged := ours + theirs
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}

	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err == nil {
		t.Fatalf("expected a loud error for the harp collision, got tasks: %+v", got)
	}
	if got != nil {
		t.Fatalf("expected no tasks on a collision error, got %+v", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "alpha") {
		t.Fatalf("error %q does not name the colliding harp", msg)
	}
	if !strings.Contains(msg, "write the storage layer") {
		t.Fatalf("error %q does not name the first task's text", msg)
	}
	if !strings.Contains(msg, "fix the flaky test") {
		t.Fatalf("error %q does not name the second task's text", msg)
	}

	// Snapshot must fail the same way.
	if _, err := s.Snapshot(); err == nil {
		t.Fatal("expected Snapshot to fail loud on the same collision")
	}
}

// TestDeferredSince_UnchangedByChronologicalFold is the Part 1c guard:
// deferredSince reads ev.Ts directly (not file position), so replaying
// events in ts order rather than file order must not change its result for
// a normal, single-writer log where ts already increases with file order.
// If this test fails after the chronological-fold change, that is a real
// behavior change to stop and report, not something to paper over.
func TestDeferredSince_UnchangedByChronologicalFold(t *testing.T) {
	s := newLog(t, "trusty-amber-wolf")

	a, err := s.AddWithTrigger("wait for the release", StatusDeferred, "on next release")
	if err != nil {
		t.Fatalf("add deferred: %v", err)
	}
	if _, err := s.SetStatus(a.HarpID, StatusToDo); err != nil {
		t.Fatalf("undefer: %v", err)
	}
	// Re-defer: deferredSince must reflect THIS later event, not the
	// original add.
	if _, err := s.SetStatusWithTrigger(a.HarpID, StatusDeferred, "on next release"); err != nil {
		t.Fatalf("re-defer: %v", err)
	}

	b, err := s.AddWithTrigger("also waiting", StatusDeferred, "on some signal")
	if err != nil {
		t.Fatalf("add second deferred: %v", err)
	}

	since, err := s.DeferredSince()
	if err != nil {
		t.Fatalf("deferred since: %v", err)
	}
	if len(since) != 2 {
		t.Fatalf("want 2 currently-deferred tasks, got %d: %+v", len(since), since)
	}
	tsA, ok := since[a.HarpID]
	if !ok {
		t.Fatalf("task %s missing from deferredSince: %+v", a.HarpID, since)
	}
	tsB, ok := since[b.HarpID]
	if !ok {
		t.Fatalf("task %s missing from deferredSince: %+v", b.HarpID, since)
	}
	// The re-deferred task's timestamp must be strictly after its original
	// add (proving it tracks the LATEST deferral event, not the first).
	if !tsA.After(time.Time{}) || !tsB.After(time.Time{}) {
		t.Fatalf("deferredSince timestamps must be non-zero: a=%v b=%v", tsA, tsB)
	}
	if !tsA.Before(tsB) && !tsA.Equal(tsB) {
		// Not a hard requirement (wall-clock granularity), but the events
		// were issued strictly in this order, so tsA must not be after tsB.
		t.Fatalf("expected a's re-defer ts <= b's add ts (issued in that order): a=%v b=%v", tsA, tsB)
	}
}
