//go:build schemagen

package schemagen

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// U097-F02: Generate with zero targets succeeded silently — MkdirAll succeeded,
// the sort was a no-op, the loop iterated zero times, and it returned nil. The
// caller then printed "gen-schemas: wrote 0 schemas" and exited 0.
//
// The trigger is concrete: both target providers sit behind `//go:build
// schemagen`, so a tag typo or a file move empties the list instead of breaking
// the build. The output directory is gitignored, no gen-schemas-check exists,
// and //go:embed all:schema still matches because resources/schema/input has
// files — so nothing downstream notices that the binary shipped with no
// schemas.
func TestGenerate_ZeroTargetsIsAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	if err := Generate(dir, nil); err == nil {
		t.Fatal("generating no schemas must not report success")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a refused run must not leave an empty output directory behind")
	}
}

func TestGenerate_WritesTheTargetsItIsGiven(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	type sample struct {
		Field string `json:"field"`
	}
	if err := Generate(dir, []Target{{Type: reflect.TypeOf(sample{}), Name: "sample"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sample-schema.json")); err != nil {
		t.Fatalf("expected the schema to be written: %v", err)
	}
}
