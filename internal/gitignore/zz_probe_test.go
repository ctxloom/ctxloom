package gitignore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestZZProbe is a scratch instrumentation probe. Not for keeps.
func TestZZProbe(t *testing.T) {
	raw, err := os.ReadFile("/tmp/probe/gitignore-pre.txt")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}

	t.Errorf("predicate(.ctxloom/*) = %v", isSupersededBlanket(".ctxloom/*"))

	changed, err := RetireSupersededFile(path)
	t.Errorf("RetireSupersededFile -> changed=%v err=%v", changed, err)

	after, _ := os.ReadFile(path)
	t.Errorf("byte-identical after retire = %v", string(after) == string(raw))

	// Now the full Ensure path as ensureHarnessGitignore drives it.
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	patterns := append(append([]string{}, PrivateStatePatterns...), TransientArtifactPatterns...)
	err = Ensure(dir, Comment, patterns...)
	after2, _ := os.ReadFile(path)
	t.Errorf("Ensure -> err=%v ; byte-identical=%v ; len before=%d after=%d",
		err, string(after2) == string(raw), len(raw), len(after2))
}
