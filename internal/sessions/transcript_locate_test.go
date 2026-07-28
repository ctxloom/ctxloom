package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeStoreFile creates a file under the harp's persist/transcripts store
// with the given mtime, mirroring an engine's nested native layout.
func writeStoreFile(t *testing.T, home, harp, rel string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(home, ".ctxloom", "sessions", harp, "persist", "transcripts", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLocateTranscript_NewestJSONLWins: discovery walks the engine's nested
// store (claude's <encoded-project>/<uuid>.jsonl, antigravity's dot-dir
// nesting) and returns the newest .jsonl.
func TestLocateTranscript_NewestJSONLWins(t *testing.T) {
	home := testsupport.Isolate(t)
	base := time.Now().Add(-time.Hour)

	writeStoreFile(t, home, "brisk-teal-otter", "-proj-enc/old.jsonl", base)
	// Antigravity nests under a DOT directory; the walk must not skip it.
	want := writeStoreFile(t, home, "brisk-teal-otter",
		"uuid-1/.system_generated/logs/transcript_full.jsonl", base.Add(30*time.Minute))
	// A newer kiro-style .json never outranks an existing .jsonl.
	writeStoreFile(t, home, "brisk-teal-otter", "meta.json", base.Add(45*time.Minute))

	got, ok := LocateTranscript("brisk-teal-otter")
	if !ok {
		t.Fatal("expected a located transcript")
	}
	if got != want {
		t.Fatalf("located %q, want %q", got, want)
	}
}

// TestLocateTranscript_SkipsSubagentInteriors: claude-code records in-harness
// subagent interiors under <session>/subagents/agent-<id>.jsonl — often the
// newest files in the store while a subagent runs. They are not the session's
// transcript, so "newest wins" must never resolve a harp to one.
func TestLocateTranscript_SkipsSubagentInteriors(t *testing.T) {
	home := testsupport.Isolate(t)
	base := time.Now().Add(-time.Hour)

	want := writeStoreFile(t, home, "brisk-teal-otter", "-proj-enc/uuid.jsonl", base)
	// The subagent interior is NEWER — and must still lose.
	writeStoreFile(t, home, "brisk-teal-otter",
		"-proj-enc/uuid/subagents/agent-a1b2c3.jsonl", base.Add(30*time.Minute))

	got, ok := LocateTranscript("brisk-teal-otter")
	if !ok {
		t.Fatal("expected a located transcript")
	}
	if got != want {
		t.Fatalf("located %q, want the main transcript %q", got, want)
	}
}

// TestLocateTranscript_SubagentOnlyStore_NeverResolves: a harp whose store
// contains ONLY a subagent interior (no main transcript at all — e.g. the
// bind hook never fired and the only structured child that ever wrote
// anything was an in-harness subagent) must resolve to nothing, not to the
// subagent file. This is the failure-path complement to
// TestLocateTranscript_SkipsSubagentInteriors (which always has a real main
// file to fall back to): it pins that fs.SkipDir on the "subagents" subtree
// genuinely removes those files from consideration rather than merely
// demoting their rank, which is what LocateTranscript's callers (Find,
// ListForProject, Reconcile, via fillTranscriptByLocation at index.go's
// production call site) depend on to avoid ever binding a harp to another
// session's private interior.
func TestLocateTranscript_SubagentOnlyStore_NeverResolves(t *testing.T) {
	home := testsupport.Isolate(t)
	base := time.Now().Add(-time.Hour)

	writeStoreFile(t, home, "brisk-teal-otter",
		"-proj-enc/uuid/subagents/agent-a1b2c3.jsonl", base)

	if got, ok := LocateTranscript("brisk-teal-otter"); ok {
		t.Fatalf("expected no transcript for a subagent-interior-only store, got %q", got)
	}
}

// TestLocateTranscript_JSONFallback: with no .jsonl in the store (the kiro
// layout), the newest .json is returned.
func TestLocateTranscript_JSONFallback(t *testing.T) {
	home := testsupport.Isolate(t)
	base := time.Now().Add(-time.Hour)

	writeStoreFile(t, home, "brisk-teal-otter", "aaa.json", base)
	want := writeStoreFile(t, home, "brisk-teal-otter", "bbb.json", base.Add(10*time.Minute))

	got, ok := LocateTranscript("brisk-teal-otter")
	if !ok || got != want {
		t.Fatalf("located %q (ok=%v), want %q", got, ok, want)
	}
}

// TestLocateTranscript_AbsentStore: no store dir (a host-native session, or an
// empty harp) degrades to not-found, never an error.
func TestLocateTranscript_AbsentStore(t *testing.T) {
	testsupport.Isolate(t)
	if _, ok := LocateTranscript("brisk-teal-otter"); ok {
		t.Fatal("expected no transcript for an absent store")
	}
	if _, ok := LocateTranscript(""); ok {
		t.Fatal("expected no transcript for an empty harp")
	}
}

// TestFind_FillsTranscriptByLocation pins the L2 mitigation: an index entry
// whose bind hook never fired (containerized structured child — TranscriptPath
// empty) resolves its transcript BY LOCATION on every read path (Find,
// ListForProject, Reconcile), computed on read and never persisted to the
// on-disk index.
func TestFind_FillsTranscriptByLocation(t *testing.T) {
	home := testsupport.Isolate(t)

	mgr, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	want := writeStoreFile(t, home, entry.HarpName, "-proj-enc/uuid.jsonl", time.Now().Add(-time.Minute))

	found, err := mgr.Find(entry.HarpName)
	if err != nil || found == nil {
		t.Fatalf("find: %v (entry=%v)", err, found)
	}
	if found.TranscriptPath != want {
		t.Fatalf("Find TranscriptPath = %q, want located %q", found.TranscriptPath, want)
	}

	listed, err := mgr.ListForProject("/proj")
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v (n=%d)", err, len(listed))
	}
	if listed[0].TranscriptPath != want {
		t.Fatalf("ListForProject TranscriptPath = %q, want %q", listed[0].TranscriptPath, want)
	}

	survivors, err := mgr.Reconcile(func(Entry) bool { return false })
	if err != nil || len(survivors) != 1 {
		t.Fatalf("reconcile: %v (n=%d)", err, len(survivors))
	}
	if survivors[0].TranscriptPath != want {
		t.Fatalf("Reconcile TranscriptPath = %q, want %q", survivors[0].TranscriptPath, want)
	}

	// Never persisted: the on-disk index still has no transcript_path.
	raw, err := os.ReadFile(mgr.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "transcript_path") {
		t.Fatalf("located path leaked into the persisted index:\n%s", raw)
	}
}

// TestFind_KeepsLiveHostBinding: an entry with a live, bound transcript is
// untouched by discovery — host non-container behavior unchanged.
func TestFind_KeepsLiveHostBinding(t *testing.T) {
	home := testsupport.Isolate(t)

	mgr, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	bound := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(bound, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.BindSession(entry.HarpName, "sess-1", bound); err != nil {
		t.Fatal(err)
	}
	// A store file exists too (both worlds populated); the live binding wins.
	writeStoreFile(t, home, entry.HarpName, "x.jsonl", time.Now())

	found, err := mgr.Find(entry.HarpName)
	if err != nil || found == nil {
		t.Fatalf("find: %v", err)
	}
	if found.TranscriptPath != bound {
		t.Fatalf("TranscriptPath = %q, want the live binding %q", found.TranscriptPath, bound)
	}
}
