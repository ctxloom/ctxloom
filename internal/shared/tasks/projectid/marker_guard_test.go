package projectid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An empty projectDir must never resolve the in-tree marker against the
// PROCESS'S CWD. paths.ProjectMarkerPath joins its argument, so "" used to
// yield the relative ".ctxloom/project-id" — which, for a taskloom invoked
// from inside some other project's tree, is that project's real marker. A
// zero-valued operations.TaskContext (WorkDir is unguarded) reaches here, so
// the failure is a silent identity swap, not a crash. Both marker entry
// points must refuse instead.
func TestMarkerRejectsEmptyProjectDir(t *testing.T) {
	// Run from a directory that DOES carry a marker, so a cwd-relative
	// resolution would visibly succeed rather than merely miss.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ctxloom", "project-id"), []byte("cwd-leaked-id\n"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	t.Chdir(dir)

	id, err := ReadMarker("")
	if err == nil {
		t.Fatalf("ReadMarker(\"\") = %q, nil — want an error, not a cwd-relative read", id)
	}
	if id != "" {
		t.Fatalf("ReadMarker(\"\") returned id %q alongside its error", id)
	}
	if !strings.Contains(err.Error(), "project directory") {
		t.Fatalf("ReadMarker(\"\") error = %v, want it to name the empty project directory", err)
	}
}

// WriteMarker("") must not create <cwd>/.ctxloom/project-id — the
// silent-no-op's write-side twin, which stamps an identity into whatever tree
// the process happens to be sitting in.
func TestWriteMarkerRejectsEmptyProjectDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := WriteMarker("", "swift-amber-falcon"); err == nil {
		t.Fatal("WriteMarker(\"\", id) = nil — want an error, not a cwd-relative write")
	}
	if _, err := os.Stat(filepath.Join(dir, ".ctxloom", "project-id")); !os.IsNotExist(err) {
		t.Fatalf("WriteMarker(\"\") created a marker under the cwd (stat err = %v)", err)
	}
}

// WriteMarker(dir, "") used to succeed, writing a file containing only "\n"
// that ReadMarker then reports as "no marker at all": exit 0, a success
// return, a file on disk, and zero identity delivered. A marker write either
// records an identity or fails; it never produces a file that means nothing.
func TestWriteMarkerRejectsEmptyID(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMarker(dir, ""); err == nil {
		t.Fatal("WriteMarker(dir, \"\") = nil — want an error, not a marker with no identity")
	}
	p := markerPathOf(t, dir)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("WriteMarker(dir, \"\") left a file at %s (stat err = %v)", p, err)
	}
}

// The same guard must reject an id ReadMarker would refuse on the way back
// in: a marker whose content the reader rejects is a write that has already
// failed, it just fails later and somewhere else.
func TestWriteMarkerRejectsCraftedID(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMarker(dir, "../../../escape"); err == nil {
		t.Fatal("WriteMarker(dir, \"../../../escape\") = nil — want the same rejection ReadMarker applies")
	}
	if _, err := os.Stat(markerPathOf(t, dir)); !os.IsNotExist(err) {
		t.Fatalf("crafted id was written to disk (stat err = %v)", err)
	}
}
