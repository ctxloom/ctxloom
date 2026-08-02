package watch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// recv waits for one event, failing on a watch error or timeout.
func recv(t *testing.T, w *Watcher) Event {
	t.Helper()
	select {
	case ev := <-w.Events():
		return ev
	case err := <-w.Errors():
		t.Fatalf("watch error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

// TestNew_FailsOnMissingRoot pins the fix: New used to silently MkdirAll the
// root it was asked to watch, so a nonexistent, typo'd, or wrongly-resolved
// root produced a healthy-looking watcher on an empty directory that streams
// zero events forever, at exit 0 — indistinguishable from a correct-but-quiet
// watch. New must fail instead; a caller that genuinely needs the directory
// to exist creates it explicitly, locally, where that intent is reviewable.
func TestNew_FailsOnMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := New(root, false, func(p string) bool { return true })
	if err == nil {
		t.Fatal("New succeeded watching a directory that does not exist")
	}
}

// A write to the single watched file in a directory is reported, and unrelated
// siblings (e.g. the .lock) are filtered out — the taskloom task-log case.
func TestWatch_FileInDir_Filtered(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "proj.jsonl")
	w, err := New(dir, false, func(p string) bool { return p == target })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// A filtered-out sibling must not produce an event before the real one.
	if err := os.WriteFile(filepath.Join(dir, "proj.jsonl.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ev := recv(t, w); ev.Path != target {
		t.Fatalf("event path = %q, want %q", ev.Path, target)
	}
}

// A *.plan.md created in a pre-existing subdirectory is reported under a
// recursive watch — the ctxloom session-plans case.
func TestWatch_Recursive_Subdir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "swift-amber-falcon")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := New(root, true, func(p string) bool { return strings.HasSuffix(p, ".plan.md") })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	plan := filepath.Join(sub, "v1.plan.md")
	if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ev := recv(t, w); ev.Path != plan {
		t.Fatalf("event path = %q, want %q", ev.Path, plan)
	}
}

// TestClose_IsIdempotent pins the fix: Close closed w.done unconditionally, so
// a SECOND call panicked with "close of closed channel". A watcher is handed to
// a caller that defers Close and to a stream that may also want to stop it, and
// neither can ask whether the other already did — an idempotent Close is the
// only shape under which "stop this watcher" is a safe thing for two owners to
// say. Close must also mean the pump has actually stopped: returning while the
// goroutine still forwards events reports a shutdown that has not happened.
func TestClose_IsIdempotent(t *testing.T) {
	w, err := New(t.TempDir(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Close returned, so the pump is stopped and the event channel is closed —
	// a receive must not block.
	select {
	case _, ok := <-w.Events():
		if ok {
			t.Fatal("Events() delivered an event after Close returned")
		}
	default:
		t.Fatal("Events() still open after Close returned: the pump had not stopped")
	}
}

// recvErr waits for one watch error, failing on timeout.
func recvErr(t *testing.T, w *Watcher) error {
	t.Helper()
	select {
	case err := <-w.Errors():
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a watch error")
	}
	return nil
}

// TestErrors_NoneAreDropped pins the fix: the errs channel is buffered at 1
// and the forward used a bare `default:`, so every watch error after the first
// undrained one was discarded with nothing said. An fsnotify error is not
// decoration — "inotify queue overflow" means events were LOST, so a dropped
// one turns a watch that is silently missing changes into a watch that looks
// healthy. Delivery must be guaranteed, not best-effort.
func TestErrors_NoneAreDropped(t *testing.T) {
	w, err := New(t.TempDir(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	first := errors.New("first watch error")
	second := errors.New("second watch error")
	// Both sends return once the pump has RECEIVED them, so by the time the
	// second returns the pump has already forwarded the first into the
	// single-slot buffer and is dealing with the second against a full one.
	w.fsw.Errors <- first
	w.fsw.Errors <- second
	// Nothing is drained until the pump has had its chance to discard: reading
	// early would free the buffer slot and hide the drop.
	time.Sleep(50 * time.Millisecond)

	if got := recvErr(t, w); !errors.Is(got, first) {
		t.Fatalf("first error = %v, want %v", got, first)
	}
	if got := recvErr(t, w); !errors.Is(got, second) {
		t.Fatalf("second error = %v, want %v", got, second)
	}
}

// TestWatch_Recursive_NewDirArrivesPopulated pins the fix: a recursive watch
// learns about a new directory only from its own Create event, by which time
// anything already inside it exists unobserved — the watch attaches to a
// directory whose contents it never saw appear, and reports nothing until they
// are touched again. That is the exact shape of a ctxloom session directory:
// the harp dir is created and its first *.plan.md written immediately, and a
// GUI subscribing to the stream would render an empty plan list indefinitely.
//
// A rename makes the window deterministic rather than racy: the directory
// becomes visible under the root already populated, so the file's creation can
// never have been observed. Adopting a new directory must therefore report what
// is already in it, not just start watching it.
func TestWatch_Recursive_NewDirArrivesPopulated(t *testing.T) {
	root := t.TempDir()
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "v1.plan.md"), []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := New(root, true, func(p string) bool { return strings.HasSuffix(p, ".plan.md") })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	session := filepath.Join(root, "swift-amber-falcon")
	if err := os.Rename(staging, session); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(session, "v1.plan.md")
	if ev := recv(t, w); ev.Path != want {
		t.Fatalf("event path = %q, want %q", ev.Path, want)
	}
}

// watchedSet returns the directories the underlying fsnotify watcher currently
// holds, as a set, so a test can assert membership by name rather than over a
// dump (order and count are both incidental).
func watchedSet(w *Watcher) map[string]bool {
	set := map[string]bool{}
	for _, d := range w.fsw.WatchList() {
		set[d] = true
	}
	return set
}

// TestAddTree_WatchesEverySubdirectoryByDefault characterizes the premise
// that a recursive watch descends unconditionally, so every directory under
// the root costs an inotify watch whether or not anything the caller asked for
// can appear in it. Measured on this machine for `ctxloom plan watch`'s root:
// 4,614 watched directories to observe 232 *.plan.md files. This is the
// baseline the SkipDir seam exists to let a caller change; it must stay true
// when no SkipDir is given, because descending everywhere is the correct
// default for a watcher that is told nothing.
func TestAddTree_WatchesEverySubdirectoryByDefault(t *testing.T) {
	root := t.TempDir()
	kept := filepath.Join(root, "harp")
	noise := filepath.Join(root, "ephemeral", "agent-worktree")
	for _, d := range []string{kept, noise} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	w, err := New(root, true, func(p string) bool { return strings.HasSuffix(p, ".plan.md") })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	watched := watchedSet(w)
	for _, d := range []string{root, kept, filepath.Dir(noise), noise} {
		if !watched[d] {
			t.Fatalf("%q is not watched; a default recursive watch descends everywhere", d)
		}
	}
}

// TestSkipDir_PrunesSubtree pins the seam that was missing: a caller
// that knows a subtree cannot contain the paths it asked for must be able to
// say so, because `filter` cannot — a directory's own name does not match the
// paths inside it, so a *.plan.md filter says nothing about whether to descend
// into an agent worktree. The pruned subtree costs no watch, at construction
// and when it appears afterwards.
func TestSkipDir_PrunesSubtree(t *testing.T) {
	root := t.TempDir()
	kept := filepath.Join(root, "harp")
	pruned := filepath.Join(root, "ephemeral")
	prunedChild := filepath.Join(pruned, "agent-worktree")
	for _, d := range []string{kept, prunedChild} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	w, err := New(root, true,
		func(p string) bool { return strings.HasSuffix(p, ".plan.md") },
		SkipDir(func(dir string) bool { return filepath.Base(dir) == "ephemeral" }),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	watched := watchedSet(w)
	if !watched[root] || !watched[kept] {
		t.Fatalf("SkipDir pruned a directory it was not asked to: root=%v kept=%v", watched[root], watched[kept])
	}
	if watched[pruned] {
		t.Fatalf("%q is watched despite SkipDir", pruned)
	}
	if watched[prunedChild] {
		t.Fatalf("%q is watched: SkipDir pruned the directory but not its subtree", prunedChild)
	}

	// A pruned directory appearing after the watch starts is pruned too: the
	// runtime adopt path is a second, separate descent.
	late := filepath.Join(kept, "ephemeral")
	if err := os.MkdirAll(filepath.Join(late, "later-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(kept, "v1.plan.md")
	if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Waiting for the plan file orders the assertion after the pump has
	// processed the directory creation that precedes it.
	if ev := recv(t, w); ev.Path != plan {
		t.Fatalf("event path = %q, want %q", ev.Path, plan)
	}
	if watchedSet(w)[late] {
		t.Fatalf("%q is watched: SkipDir was not consulted for a directory created after the watch started", late)
	}
}

// TestEvent_ReportsNoOperationKind pins the subject after the fact.
//
// A prior normalize function's `default` arm collapsed an unrecognised or empty
// fsnotify op into a confident "chmod" — absence reported as evidence. That
// function, the Op type and Event.Op are all gone: removed the whole
// vocabulary because neither consumer ever read it. Nothing pinned that
// removal, so the misreport could return unannounced with the field.
//
// The invariant the Event doc now asserts is that this watcher reports WHERE a
// change happened and says nothing about WHAT KIND it was — an fsnotify bitmask
// cannot be collapsed into one honest verb, which is why the collapse was
// wrong. Re-introducing an operation field re-opens exactly that question, and
// this test is what makes it a decision rather than an accident.
func TestEvent_ReportsNoOperationKind(t *testing.T) {
	rt := reflect.TypeFor[Event]()
	var names []string
	for i := 0; i < rt.NumField(); i++ {
		names = append(names, rt.Field(i).Name)
	}
	if len(names) != 1 || names[0] != "Path" {
		t.Fatalf("Event fields = %v, want exactly [Path]: an operation kind is back on the "+
			"event without a consumer that reads it, and no collapse of fsnotify's bitmask "+
			"into a single verb can report an unrecognised op honestly", names)
	}
}
