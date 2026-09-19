package mockengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestObservePath_UnreadableIsNotAbsent pins a real bug: observePath collapsed
// EVERY os.Stat failure into Present=false — the same answer it gives for a
// path that genuinely is not there. In the one instrument
// whose job is to prove delivery, that means a surface ctxloom DID deliver but
// which the probe could not stat reads as "ctxloom delivered nothing", and the
// test that trusted it goes red for the wrong reason — or, worse, a conformance
// suite asserting absence goes green on a failed observation.
//
// Absent, unreadable and never-asked are three states.
func TestObservePath_UnreadableIsNotAbsent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A path whose PARENT is a regular file: Stat returns ENOTDIR, which is a
	// real stat failure and is NOT IsNotExist. Deterministic, and unlike a
	// chmod it does not evaporate when the suite runs as root.
	notdir := filepath.Join(file, "child")

	rec := observePath(ProbeRecord{}, notdir, false)
	if rec.Present {
		t.Fatalf("an unstattable path must not report Present=true: %+v", rec)
	}
	if !rec.Unreadable {
		t.Fatalf("an unstattable path must be marked Unreadable — absence was never established: %+v", rec)
	}
	if rec.Note == "" {
		t.Fatalf("an unstattable path must carry a note saying why: %+v", rec)
	}

	// Control: a genuinely absent path is ordinary and must stay unmarked, or
	// every clean run starts claiming a fault.
	absent := observePath(ProbeRecord{}, filepath.Join(dir, "nope"), false)
	if absent.Present || absent.Unreadable {
		t.Fatalf("a genuinely absent path is not a fault: %+v", absent)
	}
}

// TestHashDir_WalkErrorIsReported pins a real bug: hashDir discarded WalkDir's
// error entirely (`_ = filepath.WalkDir`, plus `if err != nil { return nil }`
// inside the callback), so a directory that could not be walked produced the
// same empty listing as an empty directory — and a file that could not be READ
// was emitted as an entry with an empty SHA256, which reads like a hash nobody
// looked at rather than a read that failed.
func TestHashDir_WalkErrorIsReported(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone")

	entries, err := hashDir(missing)
	if err == nil {
		t.Fatalf("walking a directory that is not there must report the error, got entries=%v", entries)
	}
}

// TestObservePath_UnreadableEntryIsMarked pins the per-entry half: a directory
// entry whose content cannot be read must say so rather than sit in the
// listing with an empty hash.
func TestObservePath_UnreadableEntryIsMarked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as root: file mode does not deny reads")
	}
	dir := t.TempDir()
	surface := filepath.Join(dir, "commands")
	if err := os.MkdirAll(surface, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(surface, "good.md")
	if err := os.WriteFile(good, []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(surface, "bad.md")
	if err := os.WriteFile(bad, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}

	rec := observePath(ProbeRecord{}, surface, true)
	if !rec.Present || !rec.Dir {
		t.Fatalf("the directory itself is present: %+v", rec)
	}
	var badRec *EntryRecord
	for i := range rec.Entries {
		if rec.Entries[i].Name == "bad.md" {
			badRec = &rec.Entries[i]
		}
	}
	if badRec == nil {
		t.Fatalf("an unreadable entry must still be listed: %+v", rec.Entries)
	}
	if !badRec.Unreadable {
		t.Fatalf("an unreadable entry must be marked, not just left with an empty hash: %+v", *badRec)
	}
}

// TestCanonicalRendering_CarriesUnreadable proves the new state reaches the
// DIGEST. A field that exists only in the JSON but not in the one value tests
// assert on is a field the suite can never notice — which is how the collapse
// survived in the first place.
func TestCanonicalRendering_CarriesUnreadable(t *testing.T) {
	absent := canonicalRendering([]ProbeRecord{{Rel: "x", Present: false}}, nil)
	unreadable := canonicalRendering([]ProbeRecord{{Rel: "x", Present: false, Unreadable: true}}, nil)
	if absent == unreadable {
		t.Fatalf("absent and unreadable must not render identically: %q", absent)
	}

	okEntry := canonicalRendering([]ProbeRecord{{Rel: "d", Present: true, Entries: []EntryRecord{{Name: "a"}}}}, nil)
	badEntry := canonicalRendering([]ProbeRecord{{Rel: "d", Present: true, Entries: []EntryRecord{{Name: "a", Unreadable: true}}}}, nil)
	if okEntry == badEntry {
		t.Fatalf("an unreadable entry must not render like a readable one: %q", okEntry)
	}
	if !strings.Contains(badEntry, "entry|a") {
		t.Fatalf("entry rendering changed shape unexpectedly: %q", badEntry)
	}
}
