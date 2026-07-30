//go:build schemagen

package schemagen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if _, err := Generate(dir, nil); err == nil {
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
	if _, err := Generate(dir, []Target{{Type: reflect.TypeOf(sample{}), Name: "sample"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sample-schema.json")); err != nil {
		t.Fatalf("expected the schema to be written: %v", err)
	}
}

// TestGenerate_CollidingTargetNamesAreRefused pins U003-F04: two targets that
// resolve to the SAME file base silently overwrote each other on disk, and the
// caller reported one schema per TARGET — so the count could never disclose it.
// A name collision is a lying contract (one $id, two different shapes, last
// writer wins) and must be refused before anything is written.
func TestGenerate_CollidingTargetNamesAreRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	type first struct {
		A string `json:"a"`
	}
	type second struct {
		B string `json:"b"`
	}
	targets := []Target{
		{Type: reflect.TypeOf(first{}), Name: "same"},
		{Type: reflect.TypeOf(second{}), Name: "same"},
	}
	n, err := Generate(dir, targets)
	if err == nil {
		t.Fatal("two targets resolving to the same schema name must be refused, not silently collapsed")
	}
	if !strings.Contains(err.Error(), "same") {
		t.Errorf("the error must name the colliding schema, got %v", err)
	}
	if n != 0 {
		t.Errorf("a refused run reported %d schemas written", n)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "same-schema.json")); statErr == nil {
		t.Error("a refused run must not have written either colliding schema")
	}
}

// TestGenerate_ReportsFilesWrittenNotTargetsGiven pins U003-F04's other half:
// the success count comes from Generate, which knows what it wrote, rather than
// from len(targets) at the call site, which only knows what it asked for.
func TestGenerate_ReportsFilesWrittenNotTargetsGiven(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	type alpha struct {
		A string `json:"a"`
	}
	type beta struct {
		B string `json:"b"`
	}
	n, err := Generate(dir, []Target{
		{Type: reflect.TypeOf(alpha{}), Name: "alpha"},
		{Type: reflect.TypeOf(beta{}), Name: "beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(entries) {
		t.Errorf("Generate reported %d schemas written, %d files on disk", n, len(entries))
	}
}
