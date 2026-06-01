package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// expandDistillFiles resolves glob patterns and literal paths, warns on
// no-match, and errors only when nothing resolves.
func TestExpandDistillFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("name: x\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}

	t.Run("glob expands to matches", func(t *testing.T) {
		got, err := expandDistillFiles([]string{filepath.Join(dir, "*.yaml")})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %v, want 2 files", got)
		}
	})

	t.Run("literal path passes through", func(t *testing.T) {
		got, err := expandDistillFiles([]string{a})
		if err != nil || len(got) != 1 || got[0] != a {
			t.Fatalf("got %v, err %v; want [%s]", got, err, a)
		}
	})

	t.Run("no match anywhere errors", func(t *testing.T) {
		if _, err := expandDistillFiles([]string{filepath.Join(dir, "nope-*.yaml")}); err == nil {
			t.Error("expected an error when no files resolve")
		}
	})

	t.Run("missing literal is warned but a present sibling still resolves", func(t *testing.T) {
		got, err := expandDistillFiles([]string{filepath.Join(dir, "ghost.yaml"), b})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 1 || got[0] != b {
			t.Fatalf("got %v, want [%s]", got, b)
		}
	})
}
