//go:build mutation

package mutation

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The survivor ratchet (tests/mutation/survivor_ratchet.sh, driven by
// `just test-mutation-cucumber`) fails a run whose survivor count is higher
// than the count recorded for that target in survivor_baseline.txt. Its own
// silent failure mode is a row that names a target no run produces: the row is
// then never compared, and the target it was meant to guard goes unratcheted
// while the file still looks complete. These tests are the control for that,
// and they cost nothing to run:
//
//	just test-pkg ./tests/mutation/... -tags mutation -run TestSurvivorBaseline
//
// baselineGuardTarget is the one row that names a released test rather than a
// mutationTargets entry: TestTrustCascadeGuardMutation releases trust.go under
// guardNegate alone, which is a different mutant set from the stock run and so
// a different measurement with its own count.
const baselineGuardTarget = "TestTrustCascadeGuardMutation"

// baselineProvenances are the words a row may use to say what licenses its
// number. Their meanings are stated in survivor_baseline.txt itself; the set
// is pinned here so a typo cannot invent a fourth kind of trust.
var baselineProvenances = map[string]bool{
	"measured": true,
	"recorded": true,
	"stale":    true,
	"unknown":  true,
}

type baselineRow struct {
	Total      string
	Survived   string
	Provenance string
}

func readSurvivorBaseline(t *testing.T) map[string]baselineRow {
	t.Helper()

	path := filepath.Join(repoRoot(t), "tests", "mutation", "survivor_baseline.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	rows := map[string]baselineRow{}
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 4 {
			t.Errorf("line %d has %d fields, want 4 (TARGET TOTAL SURVIVED PROVENANCE): %q", i+1, len(fields), trimmed)
			continue
		}
		if _, dup := rows[fields[0]]; dup {
			t.Errorf("line %d: duplicate row for %q — one of the two would never be applied", i+1, fields[0])
		}
		rows[fields[0]] = baselineRow{Total: fields[1], Survived: fields[2], Provenance: fields[3]}
	}
	if len(rows) == 0 {
		t.Fatalf("%s holds no rows — every target would be unbaselined", path)
	}
	return rows
}

// TestSurvivorBaseline_HasARowForEveryTarget is the load-bearing one: a
// mutationTargets entry with no row is a target the ratchet cannot judge. The
// key is the TEST name, because that is what the harness prints as its
// `ooze-target:` marker and what -run addresses — so renaming an entry
// silently orphans its row, and this is what says so.
func TestSurvivorBaseline_HasARowForEveryTarget(t *testing.T) {
	rows := readSurvivorBaseline(t)

	for _, target := range mutationTargets {
		key := "TestAcceptanceMutation/" + target.Name
		if _, ok := rows[key]; !ok {
			t.Errorf("no baseline row for %q — that target's survivors are not ratcheted, and a run of it would have nothing to be judged against", key)
		}
	}
	if _, ok := rows[baselineGuardTarget]; !ok {
		t.Errorf("no baseline row for %q — the cascade guard run would be unratcheted", baselineGuardTarget)
	}
}

// TestSurvivorBaseline_NamesOnlyTargetsThatExist is the other direction: a row
// naming no released test is dead weight that makes the file look like it
// covers more than it does, and is exactly what a renamed entry leaves behind.
func TestSurvivorBaseline_NamesOnlyTargetsThatExist(t *testing.T) {
	rows := readSurvivorBaseline(t)

	known := map[string]bool{baselineGuardTarget: true}
	for _, target := range mutationTargets {
		known["TestAcceptanceMutation/"+target.Name] = true
	}

	for name := range rows {
		if !known[name] {
			t.Errorf("baseline row %q names no released test — it can never be compared against anything", name)
		}
	}
}

// TestSurvivorBaseline_RowsAreWellFormed pins the shape the ratchet parses,
// and the one thing it must never be asked to compare: an `unknown` row
// carries no numbers, and a row that carries numbers is not `unknown`.
// Survivors cannot exceed the mutant set they came from.
func TestSurvivorBaseline_RowsAreWellFormed(t *testing.T) {
	for name, row := range readSurvivorBaseline(t) {
		if !baselineProvenances[row.Provenance] {
			t.Errorf("%s: provenance %q is not one of the recognised words — the ratchet would not know what licenses the number", name, row.Provenance)
			continue
		}

		if row.Provenance == "unknown" {
			if row.Total != "-" || row.Survived != "-" {
				t.Errorf("%s: provenance is unknown but the row carries numbers (%s/%s) — say where they came from or drop them", name, row.Total, row.Survived)
			}
			continue
		}

		total, errTotal := strconv.Atoi(row.Total)
		survived, errSurvived := strconv.Atoi(row.Survived)
		if errTotal != nil || errSurvived != nil {
			t.Errorf("%s: TOTAL %q and SURVIVED %q must both be numbers when provenance is %q", name, row.Total, row.Survived, row.Provenance)
			continue
		}
		if total <= 0 {
			t.Errorf("%s: TOTAL is %d — a target that produced no mutants measured nothing, which is not a baseline", name, total)
		}
		if survived < 0 || survived > total {
			t.Errorf("%s: SURVIVED %d is not within a mutant set of %d", name, survived, total)
		}
	}
}
