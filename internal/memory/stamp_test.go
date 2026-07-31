package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// U078-F11: an unterminated frontmatter block must never corrupt the file
// (unchanged from before), but it IS a genuine failure to stamp — the doc
// comment on StampPlanFile promises "caller logs" for exactly this case, and
// the caller (hook_stamp_plan.go) only logs when err != nil. Returning nil
// here made that promise false: a stamping hook could silently do nothing,
// forever, with no diagnostic anywhere.
func TestStampPlanFile_UnterminatedFrontmatter_LeavesFileUntouchedButErrors(t *testing.T) {
	src := "---\nthis-is-not-yaml-and-no-close\n# body\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "swift-amber-falcon"); err == nil {
		t.Fatal("unterminated frontmatter must be reported as a failure to stamp, not a silent no-op")
	}
	if got := readFile(t, path); got != src {
		t.Errorf("an unterminated frontmatter block must still leave the file untouched; got:\n%s", got)
	}
}

// The malformed-YAML-but-properly-terminated case (a closing `---` exists,
// but the block between them doesn't parse) must fail the same way, for the
// same reason: "refuse to corrupt" and "silently do nothing forever" are not
// the same outcome, and only the first is intended.
func TestStampPlanFile_MalformedYAMLInTerminatedBlock_LeavesFileUntouchedButErrors(t *testing.T) {
	src := "---\n[this is not a mapping\n---\n# body\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "swift-amber-falcon"); err == nil {
		t.Fatal("malformed YAML frontmatter must be reported as a failure to stamp, not a silent no-op")
	}
	if got := readFile(t, path); got != src {
		t.Errorf("malformed frontmatter should leave file untouched; got:\n%s", got)
	}
}

func TestStampPlanFile_EmptyFrontmatter_Stamped(t *testing.T) {
	// An empty but validly-terminated frontmatter block (`---\n---\n`) must be
	// recognized and stamped, not misclassified as unterminated.
	src := "---\n---\n# body\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "swift-amber-falcon"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "---\nsessions:\n  - swift-amber-falcon\n---\n") {
		t.Errorf("empty frontmatter should be stamped with the harp; got:\n%s", got)
	}
	if !strings.Contains(got, "# body") {
		t.Errorf("body must survive; got:\n%s", got)
	}
}

func TestStampPlanFile_EmptyFrontmatterNoBody_Stamped(t *testing.T) {
	src := "---\n---\n"
	path := writePlanFile(t, "p.md", src)
	if err := StampPlanFile(path, "swift-amber-falcon"); err != nil {
		t.Fatalf("StampPlanFile: %v", err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "---\nsessions:\n  - swift-amber-falcon\n---\n") {
		t.Errorf("empty frontmatter (no body) should be stamped; got:\n%s", got)
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

// TestEncodeFrontmatter_PartialDocumentIsNeverReturned pins the contract
// updateFrontmatter depends on: the rendered string is only ever handed on to
// an atomic write over the user's plan file, so a rendering that did not
// complete must surface as an error and an EMPTY string, never as the
// truncated bytes the encoder happened to have buffered.
//
// The node below (a document whose child carries no kind) is one the yaml
// emitter refuses mid-stream: it leaves the buffer empty and puts the emitter
// in a state where the stream close also fails. Both are checked, so the
// caller cannot mistake a half-rendered document for a complete one.
func TestEncodeFrontmatter_PartialDocumentIsNeverReturned(t *testing.T) {
	broken := &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Value: "no-kind"}},
	}
	out, err := encodeFrontmatter(broken)
	if err == nil {
		t.Fatalf("expected an encode error, got out=%q", out)
	}
	if out != "" {
		t.Fatalf("a failed render must return no bytes at all, got %q", out)
	}
}

// TestEncodeFrontmatter_RoundTripsCompleteDocument is the green half: a
// well-formed frontmatter document renders in full, with nothing left
// unflushed in the encoder when the string is taken.
func TestEncodeFrontmatter_RoundTripsCompleteDocument(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("title: a plan\nsessions:\n  - one\n  - two\n"), &root); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	out, err := encodeFrontmatter(&root)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, want := range []string{"title: a plan", "sessions:", "- one", "- two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered frontmatter is missing %q:\n%s", want, out)
		}
	}
}
