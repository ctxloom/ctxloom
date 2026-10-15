package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBaseline = `# header comment, kept on update
#
# TARGET                                   TOTAL  SURVIVED  PROVENANCE
internal/a                                   100        20  measured

internal/stale                               100        20  stale
internal/new                                   -         -  unknown
`

func baselineFile(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "survivor_baseline.txt")
	if err := os.WriteFile(f, []byte(testBaseline), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// survivors is a tally with lived LIVED and notCovered NOT COVERED mutants
// among 100.
func survivors(lived, notCovered int) tally {
	return tally{killed: 100 - lived - notCovered - 5, lived: lived, notCovered: notCovered, notViable: 3, timedOut: 2}
}

// The verdict is the COUNT of unverified mutants against the row. LIVED and
// NOT COVERED both count — a mutant no test executed is as unverified as one
// a test ran past — so new code no test reaches regresses the row even when
// every mutant a test did run was killed.
func TestRatchet_JudgesTheSurvivorCountAgainstTheRow(t *testing.T) {
	cases := []struct {
		name   string
		target string
		t      tally
		want   error
		says   string
	}{
		{"held", "internal/a", survivors(15, 5), nil, "HELD"},
		{"improved is reported, not banked", "internal/a", survivors(10, 5), nil, "IMPROVED"},
		{"one more lived regresses", "internal/a", survivors(16, 5), errRegressed, ""},
		{"one more not covered regresses", "internal/a", survivors(15, 6), errRegressed, ""},
		{"a stale row still catches a regression", "internal/stale", survivors(15, 6), errRegressed, ""},
		{"an unknown row is reported, neither pass nor fail", "internal/new", survivors(40, 40), nil, "UNBASELINED"},
		{"no row is not a pass", "internal/none", survivors(0, 0), errUnbaselined, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := baselineFile(t)
			var out bytes.Buffer
			err := ratchet(&out, f, c.target, c.t, false)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if !strings.Contains(out.String(), c.says) {
				t.Errorf("output %q, want it to say %q", out.String(), c.says)
			}
			if b, _ := os.ReadFile(f); string(b) != testBaseline {
				t.Errorf("a judging run rewrote the baseline:\n%s", b)
			}
		})
	}
}

// A run that measured nothing is refused before it is judged or recorded:
// zero mutants, or none that compiled, has zero survivors and would read as
// an improvement on any row.
func TestRatchet_RefusesARunThatMeasuredNothing(t *testing.T) {
	for name, tl := range map[string]tally{
		"zero mutants":          {},
		"nothing compiled":      {notViable: 7},
		"nothing compiled, too": {notViable: 1},
	} {
		for _, update := range []bool{false, true} {
			f := baselineFile(t)
			if err := ratchet(&bytes.Buffer{}, f, "internal/a", tl, update); !errors.Is(err, errMeasuredNothing) {
				t.Errorf("%s (update %v): err = %v, want errMeasuredNothing", name, update, err)
			}
			if b, _ := os.ReadFile(f); string(b) != testBaseline {
				t.Errorf("%s (update %v): the baseline was rewritten:\n%s", name, update, b)
			}
		}
	}
}

// -update rewrites the package's row in place as measured — or appends a new
// one — and leaves every other line, comments included, as it was.
func TestRatchet_UpdateRecordsTheRow(t *testing.T) {
	f := baselineFile(t)
	for _, c := range []struct {
		target string
		t      tally
		says   string
	}{
		{"internal/a", survivors(15, 5), "RECORDED  internal/a: 20 survivors of 100 (was 20 of 100, measured)"},
		{"internal/new", survivors(30, 10), "RECORDED  internal/new: 40 survivors of 100 (was - of -, unknown)"},
		{"internal/added", survivors(1, 2), "RECORDED  internal/added: 3 survivors of 100 (new row)"},
	} {
		var out bytes.Buffer
		if err := ratchet(&out, f, c.target, c.t, true); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), c.says) {
			t.Errorf("update of %s said %q, want %q", c.target, out.String(), c.says)
		}
	}
	rows, err := readBaseline(f)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]baselineRow{
		"internal/a":     {100, 20, "measured"},
		"internal/stale": {100, 20, "stale"},
		"internal/new":   {100, 40, "measured"},
		"internal/added": {100, 3, "measured"},
	}
	for k, w := range want {
		if rows[k] != w {
			t.Errorf("row %s = %+v, want %+v", k, rows[k], w)
		}
	}
	b, _ := os.ReadFile(f)
	if !strings.HasPrefix(string(b), "# header comment, kept on update\n") {
		t.Errorf("the header was not kept:\n%s", b)
	}
	if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the rewritten baseline's mode = %v, %v; want 0644", fi.Mode().Perm(), err)
	}
}

// A malformed row is refused, not skipped: a skipped row would turn its
// package into "unbaselined" and its regression into a message.
func TestReadBaseline_RefusesMalformedRows(t *testing.T) {
	for _, line := range []string{
		"internal/a 10 2",
		"internal/a 10 2 guessed",
		"internal/a ten 2 measured",
		"internal/a 2 10 measured",
		"internal/a 10 2 unknown",
		"internal/a - 2 unknown",
		"internal/a 2 - unknown",
		"internal/a - - measured",
		"internal/a -1 0 measured",
		"internal/a 1 -1 measured",
	} {
		f := filepath.Join(t.TempDir(), "b.txt")
		if err := os.WriteFile(f, []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readBaseline(f); !errors.Is(err, errBaselineFormat) {
			t.Errorf("%q: err = %v, want errBaselineFormat", line, err)
		}
	}
	f := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(f, []byte("internal/a 1 0 measured\ninternal/a 2 0 measured\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readBaseline(f)
	if !errors.Is(err, errBaselineFormat) {
		t.Errorf("a duplicated row: err = %v, want errBaselineFormat", err)
	}
	// The refusal names the line, so the row can be found and fixed.
	if err == nil || !strings.Contains(err.Error(), "b.txt:2:") {
		t.Errorf("a duplicate on line 2: err = %v, want it to name b.txt:2", err)
	}
	for line, body := range map[int]string{
		1: "internal/a 1 0 guessed\n",
		2: "# c\ninternal/a - 2 unknown\n",
		3: "#\n\ninternal/a 2 3 measured\n",
	} {
		f := filepath.Join(t.TempDir(), "b.txt")
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readBaseline(f); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("b.txt:%d:", line)) {
			t.Errorf("%q: err = %v, want it to name b.txt:%d", body, err, line)
		}
	}
}

// The edges of a well-formed row are rows: zero counts, and every mutant a
// survivor.
func TestReadBaseline_AcceptsTheEdges(t *testing.T) {
	f := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(f, []byte("internal/z 0 0 measured\ninternal/all 5 5 stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := readBaseline(f)
	if err != nil || rows["internal/z"] != (baselineRow{0, 0, "measured"}) || rows["internal/all"] != (baselineRow{5, 5, "stale"}) {
		t.Errorf("rows = %+v, %v", rows, err)
	}
}

// A ratchet over a baseline it cannot read refuses, rather than judging
// against no rows.
func TestRatchet_RefusesAMalformedBaseline(t *testing.T) {
	f := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(f, []byte("internal/a 10 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, update := range []bool{false, true} {
		if err := ratchet(&bytes.Buffer{}, f, "internal/a", survivors(1, 1), update); !errors.Is(err, errBaselineFormat) {
			t.Errorf("update %v: err = %v, want errBaselineFormat", update, err)
		}
	}
}

// The project's own baseline parses, and every row names a package directory
// of this module: a row for a moved or deleted package would never be judged.
func TestSurvivorBaseline_RowsNameModulePackages(t *testing.T) {
	root := filepath.Join("..", "..")
	rows, err := readBaseline(filepath.Join(root, "tests", "mutation", "survivor_baseline.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("the survivor baseline has no rows")
	}
	for target := range rows {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(target), "*.go"))
		if len(matches) == 0 {
			t.Errorf("row %s names no Go package directory of this module", target)
		}
	}
}
