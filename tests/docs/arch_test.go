//go:build arch

// Package docs holds gates over the repository's own long-lived record files.
//
// The findings index is maintained by hand across many remediation batches, and
// its header totals have now drifted three separate times — understated by 37
// once and by one twice — each time costing the next batch a manual recount.
// A number that is re-derived from the rows on every test run cannot drift.
package docs

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// indexPath locates docs/architecture/findings-index.md by walking up from the
// test's working directory to the module root, so the gate does not care where
// it is run from.
func indexPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "docs", "architecture", "findings-index.md")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root (no go.mod above the working directory)")
		}
		dir = parent
	}
}

// tally is the recomputed state of the index: rows per severity section, and
// rows per status within each.
type tally struct {
	// bySeverity maps a section heading ("HIGH") to its row count.
	bySeverity map[string]int
	// byStatus maps a status word ("open", "RESOLVED") to its row count across
	// the whole file.
	byStatus map[string]int
	// total is every row in every section.
	total int
	// resolvedBySeverity counts only rows marked RESOLVED, per section.
	resolvedBySeverity map[string]int
}

var (
	sectionRe = regexp.MustCompile(`^## (HIGH|MED|LOW|Unparsed severity) \((\d+)\)\s*$`)
	rowRe     = regexp.MustCompile(`^\|\s*(U\d+-F\d+)\s*\|\s*([^|]*?)\s*\|`)
	// statusRe pulls the status WORD out of a status cell, which may be
	// "open", "**RESOLVED** `sha`", "**PARTIAL** `sha`", etc.
	statusRe = regexp.MustCompile(`^\**([A-Za-z]+)\**`)
)

// parseIndex recomputes the tally from the rows, and returns the counts the
// section headings CLAIM so the two can be compared.
func parseIndex(t *testing.T, body string) (got tally, claimed map[string]int) {
	t.Helper()
	got = tally{
		bySeverity:         map[string]int{},
		byStatus:           map[string]int{},
		resolvedBySeverity: map[string]int{},
	}
	claimed = map[string]int{}

	section := ""
	seen := map[string]string{}
	for i, line := range strings.Split(body, "\n") {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			section = m[1]
			n, err := strconv.Atoi(m[2])
			if err != nil {
				t.Fatalf("line %d: unparseable section count %q", i+1, m[2])
			}
			claimed[section] = n
			continue
		}
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if section == "" {
			t.Fatalf("line %d: finding row %q appears before any severity section", i+1, m[1])
		}
		// A duplicated ID would inflate a count silently and make "is this row
		// still open?" ambiguous — the exact question every batch starts from.
		if prev, dup := seen[m[1]]; dup {
			t.Errorf("duplicate finding ID %s (already in section %s, again at line %d)", m[1], prev, i+1)
		}
		seen[m[1]] = section

		status := "open"
		if sm := statusRe.FindStringSubmatch(strings.TrimSpace(m[2])); sm != nil {
			status = sm[1]
		}
		got.bySeverity[section]++
		got.byStatus[status]++
		got.total++
		if status == "RESOLVED" {
			got.resolvedBySeverity[section]++
		}
	}
	return got, claimed
}

// TestArch_FindingsIndex_SectionHeadingsMatchTheirRows is the gate proper: each
// "## HIGH (376)" heading must equal the number of rows beneath it.
func TestArch_FindingsIndex_SectionHeadingsMatchTheirRows(t *testing.T) {
	body := readIndex(t)
	got, claimed := parseIndex(t, body)

	if len(claimed) == 0 {
		t.Fatal("no severity sections found — the index format changed and this gate has stopped looking at anything")
	}
	for _, sec := range slices.Sorted(maps.Keys(claimed)) {
		if claimed[sec] != got.bySeverity[sec] {
			t.Errorf("## %s claims (%d) but has %d rows — recount, do not adjust by hand", sec, claimed[sec], got.bySeverity[sec])
		}
	}
}

// TestArch_FindingsIndex_StatusTableMatchesTheRows checks the per-status counts in
// the "Status — what happened to each row" table, which is the table that has
// actually drifted.
func TestArch_FindingsIndex_StatusTableMatchesTheRows(t *testing.T) {
	body := readIndex(t)
	got, _ := parseIndex(t, body)

	// Rows look like: | **RESOLVED** `<sha>` | a commit named … | **225** |
	// and:            | `open` | no commit names this ID | **2,039** |
	statusRowRe := regexp.MustCompile("^\\|\\s*[`*]*([A-Za-z]+)[`*]*[^|]*\\|[^|]*\\|\\s*\\**([\\d,]+)\\**\\s*\\|")
	found := 0
	for _, line := range strings.Split(body, "\n") {
		m := statusRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		status := m[1]
		if _, isStatus := got.byStatus[status]; !isStatus && status != "open" {
			continue // some other two-column table
		}
		claimed, err := strconv.Atoi(strings.ReplaceAll(m[2], ",", ""))
		if err != nil {
			continue
		}
		found++
		if claimed != got.byStatus[status] {
			t.Errorf("status table claims %d %s rows, the rows say %d", claimed, status, got.byStatus[status])
		}
	}
	if found == 0 {
		t.Fatal("no status-table rows recognised — the table format changed and this gate has stopped looking at anything")
	}
}

// TestArch_FindingsIndex_TotalsLineMatchesTheRows checks the prose "Totals:" line,
// which is what a reader actually quotes.
func TestArch_FindingsIndex_TotalsLineMatchesTheRows(t *testing.T) {
	body := readIndex(t)
	got, _ := parseIndex(t, body)

	totalsRe := regexp.MustCompile(`\*\*Totals: ([\d,]+) findings across (\d+) units — ([\d,]+) resolved, ([\d,]+) still open`)
	m := totalsRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no **Totals:** line found — the gate has stopped looking at anything")
	}
	check := func(label, claimed string, want int) {
		n, err := strconv.Atoi(strings.ReplaceAll(claimed, ",", ""))
		if err != nil {
			t.Fatalf("unparseable %s in the Totals line: %q", label, claimed)
		}
		if n != want {
			t.Errorf("Totals line claims %d %s, the rows say %d", n, label, want)
		}
	}
	check("findings", m[1], got.total)
	check("resolved", m[3], got.byStatus["RESOLVED"])
	check("still open", m[4], got.byStatus["open"])
}

// TestArch_FindingsIndex_SeverityTableMatchesTheRows checks the per-severity
// count/resolved/open table. Cells may carry a trailing annotation
// ("275 (+1 refuted)"); only the leading integer is compared, because the
// annotation is prose about the adjudicated rows and is not itself a count.
func TestArch_FindingsIndex_SeverityTableMatchesTheRows(t *testing.T) {
	body := readIndex(t)
	got, _ := parseIndex(t, body)

	rowRe := regexp.MustCompile(`^\|\s*(HIGH|MED|LOW)\s*\|\s*([\d,]+)\s*\|\s*([\d,]+)\s*\|\s*([\d,]+)`)
	found := 0
	for _, line := range strings.Split(body, "\n") {
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		found++
		sec := m[1]
		num := func(s string) int {
			n, _ := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
			return n
		}
		if num(m[2]) != got.bySeverity[sec] {
			t.Errorf("severity table claims %s count=%d, the rows say %d", sec, num(m[2]), got.bySeverity[sec])
		}
		if num(m[3]) != got.resolvedBySeverity[sec] {
			t.Errorf("severity table claims %s resolved=%d, the rows say %d", sec, num(m[3]), got.resolvedBySeverity[sec])
		}
	}
	if found != 3 {
		t.Fatalf("expected 3 severity rows (HIGH/MED/LOW), recognised %d — the table format changed", found)
	}
}

// TestArch_FindingsIndex_GateCatchesDrift proves the gate can FAIL. A gate that has
// only ever been run against a correct file is a gate nobody has tested; this
// runs the same parser over a doctored copy and requires a mismatch to be
// reported. Without it, a parser that silently matched nothing would pass
// every check above forever.
func TestArch_FindingsIndex_GateCatchesDrift(t *testing.T) {
	const good = "## HIGH (2)\n\n| ID | Status |\n|---|---|\n| U001-F01 | open |\n| U001-F02 | **RESOLVED** `abc1234` |\n"

	got, claimed := parseIndex(t, good)
	if got.bySeverity["HIGH"] != 2 || claimed["HIGH"] != 2 {
		t.Fatalf("control: parser miscounted a known-good fixture: %+v / %+v", got, claimed)
	}
	if got.byStatus["open"] != 1 || got.byStatus["RESOLVED"] != 1 {
		t.Fatalf("control: parser misread statuses: %+v", got.byStatus)
	}

	// Understated heading — the exact drift that has happened three times.
	drifted := strings.Replace(good, "## HIGH (2)", "## HIGH (1)", 1)
	dGot, dClaimed := parseIndex(t, drifted)
	if dClaimed["HIGH"] == dGot.bySeverity["HIGH"] {
		t.Fatal("the gate does not notice an understated section heading")
	}

	// A row that flipped to RESOLVED without the totals being recounted.
	flipped := strings.Replace(good, "| U001-F01 | open |", "| U001-F01 | **RESOLVED** `def5678` |", 1)
	fGot, _ := parseIndex(t, flipped)
	if fGot.byStatus["open"] != 0 || fGot.byStatus["RESOLVED"] != 2 {
		t.Fatalf("the gate does not track a status flip: %+v", fGot.byStatus)
	}
}

func readIndex(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(indexPath(t))
	if err != nil {
		t.Fatalf("read findings index: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("findings index is empty")
	}
	return string(b)
}
