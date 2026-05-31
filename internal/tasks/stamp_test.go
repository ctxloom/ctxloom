package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlanFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestStampPlanFile_NoFrontmatter_PrependsBlock(t *testing.T) {
	path := writePlanFile(t, "current_plan.md", "# Plan\n\nbody here\n")
	if err := StampPlanFile(path, "swift-amber-falcon"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "---\nsessions:\n  - swift-amber-falcon\n---\n") {
		t.Errorf("expected frontmatter prepended; got:\n%s", got)
	}
	if !strings.Contains(got, "# Plan\n\nbody here") {
		t.Errorf("body should survive verbatim; got:\n%s", got)
	}
}

func TestStampPlanFile_ExistingFrontmatter_NoSessions_AppendsKey(t *testing.T) {
	src := "---\ntitle: Some Plan\nauthor: ctxloom\n---\n\n# body\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "quiet-silver-meadow"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "title: Some Plan") {
		t.Errorf("existing keys must survive; got:\n%s", got)
	}
	if !strings.Contains(got, "sessions:\n    - quiet-silver-meadow") &&
		!strings.Contains(got, "sessions:\n  - quiet-silver-meadow") {
		t.Errorf("sessions key should be appended with our harp; got:\n%s", got)
	}
	if !strings.Contains(got, "# body") {
		t.Errorf("body must survive; got:\n%s", got)
	}
}

func TestStampPlanFile_ExistingSessions_AppendsHarp(t *testing.T) {
	src := "---\nsessions:\n  - bold-crimson-thunder\n---\n\nbody\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "swift-amber-falcon"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "bold-crimson-thunder") {
		t.Errorf("existing session must survive; got:\n%s", got)
	}
	if !strings.Contains(got, "swift-amber-falcon") {
		t.Errorf("new session must be added; got:\n%s", got)
	}
}

func TestStampPlanFile_HarpAlreadyPresent_NoOp(t *testing.T) {
	src := "---\nsessions:\n  - swift-amber-falcon\n  - quiet-silver-meadow\n---\nbody\n"
	path := writePlanFile(t, "p.md", src)
	infoBefore, _ := os.Stat(path)
	if err := StampPlanFile(path, "swift-amber-falcon"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	got := readFile(t, path)
	// Content should be byte-identical.
	if got != src {
		t.Errorf("idempotent stamp should not modify file; got:\n%s", got)
	}
	infoAfter, _ := os.Stat(path)
	// mtime check is a hint, not a contract (some filesystems coalesce).
	_ = infoBefore
	_ = infoAfter
}

func TestStampPlanFile_MalformedFrontmatter_NoOp(t *testing.T) {
	// Opening `---` but no closing — must not corrupt.
	src := "---\nthis-is-not-yaml-and-no-close\n# body\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "swift-amber-falcon"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	if got := readFile(t, path); got != src {
		t.Errorf("malformed frontmatter should leave file untouched; got:\n%s", got)
	}
}

func TestStampPlanFile_EmptyHarp_Errors(t *testing.T) {
	path := writePlanFile(t, "p.md", "body\n")
	if err := StampPlanFile(path, ""); err == nil {
		t.Error("empty harpName should error")
	}
}

func TestStampPlanFile_MissingFile_Errors(t *testing.T) {
	if err := StampPlanFile("/no/such/file.md", "swift-amber-falcon"); err == nil {
		t.Error("missing file should error")
	}
}

func TestStampPlanFile_RoundTripPreservesArbitraryFields(t *testing.T) {
	src := "---\nstatus: draft\nowner:\n  name: ada\nsessions:\n  - bold-crimson-thunder\n---\n\nbody\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "swift-amber-falcon"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	got := readFile(t, path)
	for _, want := range []string{"status: draft", "owner:", "name: ada", "bold-crimson-thunder", "swift-amber-falcon"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output:\n%s", want, got)
		}
	}
}
