package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// THE SURVIVOR RATCHET: a whole-package mutation run that leaves MORE
// mechanisms unverified than the package's recorded row must not pass.
//
// The efficacy threshold names the project standard, and a standing package
// that misses it would red every run — a gate that reds on recorded debt is
// never run. So for a ratcheted package the threshold is reported, and the
// verdict is this: did the count of survivors (LIVED plus NOT COVERED — a
// mutant no test executed is as unverified as one a test ran past) grow?
// The count, not the score: code added with good tests raises the score while
// leaving more mechanisms unverified, and the count cannot be moved that way.
// TOTAL sits beside it only so a failure can say whether the survivors arrived
// with a bigger mutant set; nothing is gated on it.
//
// The rows live in tests/mutation/survivor_baseline.txt, keyed by the
// package's module-relative directory:
//
//	TARGET  TOTAL  SURVIVED  PROVENANCE
//
// PROVENANCE says what licenses the number: `measured` (a run of this gate
// wrote it), `stale` (known not to describe HEAD; an upper bound, so it still
// catches a regression), or `unknown` (no number exists — TOTAL and SURVIVED
// are "-", and the run is reported, neither passed nor failed).

var (
	errRegressed      = errors.New("the package left more mutants unverified than its baseline row")
	errUnbaselined    = errors.New("the package has no baseline row")
	errRatchetScope   = errors.New("the ratchet judges a whole package: -scope tree with -pkg")
	errBaselineFormat = errors.New("malformed survivor baseline")
)

const provenanceUnknown = "unknown"

var provenances = map[string]bool{"measured": true, "stale": true, provenanceUnknown: true}

// baselineRow is one recorded package.
type baselineRow struct {
	total, survived int
	provenance      string
}

// readBaseline parses the baseline file's rows by target.
func readBaseline(file string) (map[string]baselineRow, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	rows := map[string]baselineRow{}
	for i, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if len(f) != 4 || !provenances[f[3]] {
			return nil, fmt.Errorf("%w: %s:%d: want TARGET TOTAL SURVIVED %s, got %q", errBaselineFormat, file, i+1, "measured|stale|unknown", line)
		}
		row := baselineRow{provenance: f[3]}
		if f[3] == provenanceUnknown {
			if f[1] != "-" || f[2] != "-" {
				return nil, fmt.Errorf("%w: %s:%d: an unknown row records no counts (\"-\"), got %q", errBaselineFormat, file, i+1, line)
			}
		} else {
			if row.total, err = strconv.Atoi(f[1]); err == nil {
				row.survived, err = strconv.Atoi(f[2])
			}
			if err != nil || row.total < 0 || row.survived < 0 || row.survived > row.total {
				return nil, fmt.Errorf("%w: %s:%d: counts must be whole numbers with SURVIVED <= TOTAL, got %q", errBaselineFormat, file, i+1, line)
			}
		}
		if _, dup := rows[f[0]]; dup {
			return nil, fmt.Errorf("%w: %s:%d: %s has more than one row", errBaselineFormat, file, i+1, f[0])
		}
		rows[f[0]] = row
	}
	return rows, nil
}

// ratchet judges target's tally against its row in file, or, with update,
// records the tally as the row. It refuses a run that measured nothing before
// either: zero mutants, or none that compiled, would read as a perfect score.
func ratchet(stdout io.Writer, file, target string, t tally, update bool) error {
	if t.total() == 0 {
		return fmt.Errorf("%w: %s produced ZERO mutants", errMeasuredNothing, target)
	}
	if t.notViable == t.total() {
		return fmt.Errorf("%w: all %d mutants of %s were NOT VIABLE — the tree does not build once mutated", errMeasuredNothing, t.total(), target)
	}
	rows, err := readBaseline(file)
	if err != nil {
		return err
	}
	row, ok := rows[target]
	if update {
		if err := writeRow(file, target, t); err != nil {
			return err
		}
		was := "new row"
		if ok {
			was = fmt.Sprintf("was %s of %s, %s", count(row.survived, row), count(row.total, row), row.provenance)
		}
		_, _ = fmt.Fprintf(stdout, "RECORDED  %s: %d survivors of %d (%s)\n", target, t.survived(), t.total(), was)
		return nil
	}
	switch {
	case !ok:
		return fmt.Errorf("%w: %s, in %s — an unbaselined measurement is not a pass; record it with CTXLOOM_MUTATION_BASELINE=update", errUnbaselined, target, file)
	case row.provenance == provenanceUnknown:
		_, _ = fmt.Fprintf(stdout, "UNBASELINED  %s: %d survivors of %d. No count was ever recorded, so this run is neither a pass nor a failure; record it with CTXLOOM_MUTATION_BASELINE=update.\n", target, t.survived(), t.total())
	case t.survived() > row.survived:
		return fmt.Errorf("%w: %s: %d survivors -> %d (mutant set %d -> %d). Kill the new survivors, or raise the row in %s in this same change with the reason",
			errRegressed, target, row.survived, t.survived(), row.total, t.total(), file)
	case t.survived() < row.survived:
		_, _ = fmt.Fprintf(stdout, "IMPROVED  %s: %d survivors -> %d. Lower the row in %s in this same change, or the ratchet banks the slack.\n", target, row.survived, t.survived(), file)
	default:
		_, _ = fmt.Fprintf(stdout, "HELD  %s: %d survivors of %d (baseline %d, %s)\n", target, t.survived(), t.total(), row.survived, row.provenance)
	}
	return nil
}

// count renders a row's number, "-" for an unknown row's.
func count(n int, row baselineRow) string {
	if row.provenance == provenanceUnknown {
		return "-"
	}
	return strconv.Itoa(n)
}

// writeRow rewrites target's row in place, or appends it, marking it measured;
// every other line, comments included, is kept as it was.
func writeRow(file, target string, t tally) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	row := fmt.Sprintf("%-42s %5d %9d  measured", target, t.total(), t.survived())
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	done := false
	for i, line := range lines {
		if f := strings.Fields(line); len(f) > 0 && f[0] == target {
			lines[i], done = row, true
		}
	}
	if !done {
		lines = append(lines, row)
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".baseline-")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if _, err := tmp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
