package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// shouldSkipDistill gates one item: a no_distill item is always skipped; an
// unchanged item is skipped unless --force; a changed item proceeds.
func TestShouldSkipDistill(t *testing.T) {
	prev := bundleDistillForce
	defer func() { bundleDistillForce = prev }()

	t.Run("no_distill always skips", func(t *testing.T) {
		bundleDistillForce = false
		if !shouldSkipDistill("fragment", "x", true, true) {
			t.Error("no_distill item should be skipped")
		}
	})

	t.Run("unchanged item skips without force", func(t *testing.T) {
		bundleDistillForce = false
		if !shouldSkipDistill("fragment", "x", false, false) {
			t.Error("unchanged item should be skipped without --force")
		}
	})

	t.Run("unchanged item proceeds with force", func(t *testing.T) {
		bundleDistillForce = true
		if shouldSkipDistill("fragment", "x", false, false) {
			t.Error("--force should re-distill an unchanged item")
		}
	})

	t.Run("changed item proceeds", func(t *testing.T) {
		bundleDistillForce = false
		if shouldSkipDistill("prompt", "x", false, true) {
			t.Error("a changed (NeedsDistill) item should not be skipped")
		}
	})

	t.Run("no_distill wins over force", func(t *testing.T) {
		bundleDistillForce = true
		if !shouldSkipDistill("fragment", "x", true, true) {
			t.Error("no_distill should skip even under --force")
		}
	})
}

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
