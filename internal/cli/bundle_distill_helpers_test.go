package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
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

// countDistillItems and printDistillItems replaced the old renderDistillItems
// (which printed AND counted in one pass) once bundle distill started
// buffering its result for emit(). Pin that the split kept both halves
// correct: printing is now purely a function of the items (no counting side
// effect), and counting has no output side effect.
func TestCountDistillItems_TalliesByStatus(t *testing.T) {
	items := []operations.DistillBundleItem{
		{Status: operations.DistillStatusDistilled},
		{Status: operations.DistillStatusPlanned},
		{Status: operations.DistillStatusSkipped},
		{Status: operations.DistillStatusSkipped},
	}
	processed, skipped := countDistillItems(items)
	assert.Equal(t, 2, processed)
	assert.Equal(t, 2, skipped)
}

func TestPrintDistillItems_OneLinePerItem(t *testing.T) {
	var buf bytes.Buffer
	printDistillItems(&buf, []operations.DistillBundleItem{
		{Kind: operations.ItemKindFragment, Name: "a", Status: operations.DistillStatusDistilled, ModelID: "m1"},
		{Kind: operations.ItemKindCommand, Name: "b", Status: operations.DistillStatusSkipped, Reason: "unchanged"},
		{Kind: operations.ItemKindFragment, Name: "c", Status: operations.DistillStatusPlanned},
	})
	out := buf.String()
	assert.Contains(t, out, "Distilled fragment: a (m1)")
	assert.Contains(t, out, "Skipping command b (unchanged)")
	assert.Contains(t, out, "Would distill fragment: c")
}

func TestPrintDistillSummary_DryRunReportsWouldDistillCount(t *testing.T) {
	var buf bytes.Buffer
	printDistillSummary(&buf, 3, 0, 0, true)
	assert.Contains(t, buf.String(), "Dry run: would distill 3 items")
}

func TestPrintDistillSummary_NoItemsReportsNothingToDistill(t *testing.T) {
	var buf bytes.Buffer
	printDistillSummary(&buf, 0, 0, 0, false)
	assert.Contains(t, buf.String(), "No items to distill.")
}
