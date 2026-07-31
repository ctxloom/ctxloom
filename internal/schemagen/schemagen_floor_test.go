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

// TestGenerate_DoesNotPruneStaleSchemas characterizes U003-F05's mechanism,
// which is real: Generate only ever writes, so a schema for a result type that
// has since been renamed or deleted survives in the output directory
// indefinitely. The claim's CONSEQUENCE — that //go:embed then ships the stale
// file inside the binary — no longer holds: resources/embed.go embeds
// schema/input explicitly, not all:schema, and
// TestEmbeddedFS_ExcludesGeneratedSchemas pins that.
//
// So the survival below is stated behaviour, not an accident, and the reported
// count is of files WRITTEN by this run — never of files present in the
// directory, which is what would make a stale one look generated.
func TestGenerate_DoesNotPruneStaleSchemas(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "retired-result-schema.json")
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	type current struct {
		A string `json:"a"`
	}
	n, err := Generate(dir, []Target{{Type: reflect.TypeOf(current{}), Name: "current"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Generate reported %d written, want 1 — the count must not include files it found", n)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("the generator started pruning: %v — that is a deliberate change, not a silent one", err)
	}
}

// TestGenerate_DoesNotReorderTheCallersSlice pins U097-F03: Generate sorted the
// targets slice it was handed, and sort.Slice sorts the caller's backing array
// in place. A caller that builds a target list, hands it over, and then reads
// it back (to report on it, to diff it, to hand the same slice to a second
// generator) silently observes a DIFFERENT order than it constructed — an
// undocumented side effect on an argument the signature gives no hint is
// mutable. Generate's own deterministic processing order is worth keeping, so
// the ordering happens on a copy; only the mutation is the defect.
func TestGenerate_DoesNotReorderTheCallersSlice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	type sample struct {
		A string `json:"a"`
	}
	// Deliberately supplied in reverse of the order Generate processes them.
	targets := []Target{
		{Type: reflect.TypeOf(sample{}), Name: "zulu"},
		{Type: reflect.TypeOf(sample{}), Name: "alpha"},
	}
	want := []string{"zulu", "alpha"}

	if _, err := Generate(dir, targets); err != nil {
		t.Fatal(err)
	}

	// Assert the fixture reached the code under test at all: if Generate had
	// bailed before the ordering step, the slice would be untouched for a
	// reason that has nothing to do with the defect (§11k).
	for _, n := range want {
		if _, err := os.Stat(filepath.Join(dir, n+"-schema.json")); err != nil {
			t.Fatalf("%s-schema.json was not written, so this test never reached the ordering step: %v", n, err)
		}
	}

	for i, n := range want {
		if targets[i].Name != n {
			t.Fatalf("Generate reordered the caller's slice: position %d is %q, want %q", i, targets[i].Name, n)
		}
	}
}
