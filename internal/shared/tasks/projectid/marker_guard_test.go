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
