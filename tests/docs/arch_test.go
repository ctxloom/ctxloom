//go:build arch

// Package docs holds gates over the repository's own long-lived record files.
//
// The findings index is maintained by hand across many remediation batches, and
// its header totals have now drifted three separate times — understated by 37
// once and by one twice — each time costing the next batch a manual recount.
// A number that is re-derived from the rows on every test run cannot drift.
package docs

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	// byStatusBySeverity counts rows per status WITHIN each section, so the
	// severity table can be checked column by column instead of inferring
	// "open" as count-minus-resolved. That inference silently folded PARTIAL,
	// REFUTED and ESCALATED into "open" while the status table above used
	// "open" to mean the status — one word, two meanings, in one document.
	byStatusBySeverity map[string]map[string]int
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
		byStatusBySeverity: map[string]map[string]int{},
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
		if got.byStatusBySeverity[section] == nil {
			got.byStatusBySeverity[section] = map[string]int{}
		}
		got.byStatusBySeverity[section][status]++
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

// flowPartitionPath locates docs/architecture/flow-partition.yaml the same
// way indexPath locates the census, so the gate does not care where it is
// run from.
func flowPartitionPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "docs", "architecture", "flow-partition.yaml")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root (no go.mod above the working directory)")
		}
		dir = parent
	}
}

// unitIDRe pulls the unit portion ("U028") out of a finding row's ID
// ("U028-F01"), so a unit can be checked for having ANY row regardless of how
// many findings it carries.
var unitIDRe = regexp.MustCompile(`^\|\s*(U\d+)-F\d+\s*\|`)

// unitsWithRows returns every unit that carries at least one row anywhere in
// the census (including the "Unparsed severity" tail section, which shares
// the same ID-first row shape).
func unitsWithRows(body string) map[string]bool {
	units := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if m := unitIDRe.FindStringSubmatch(line); m != nil {
			units[m[1]] = true
		}
	}
	return units
}

// TestArch_FlowPartition_IsTotalAndDisjoint gates
// docs/architecture/flow-partition.yaml, the 14-way partition the
// remediation campaign uses to divide the census by flow. A campaign that
// reports "flow X: N rows closed" is unverifiable unless the partition it
// counted from is itself provably TOTAL (every unit that carries a row is
// claimed by some flow) and DISJOINT (no unit is claimed by two flows) —
// otherwise a per-flow count can silently double-count or drop units.
func TestArch_FlowPartition_IsTotalAndDisjoint(t *testing.T) {
	body := readIndex(t)
	units := unitsWithRows(body)
	if len(units) == 0 {
		t.Fatal("no unit IDs found in the census — the gate has stopped looking at anything")
	}

	raw, err := os.ReadFile(flowPartitionPath(t))
	if err != nil {
		t.Fatalf("read flow partition: %v", err)
	}
	var doc struct {
		Flows map[string][]string `yaml:"flows"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse flow partition YAML: %v", err)
	}
	if len(doc.Flows) == 0 {
		t.Fatal("no flows found in flow-partition.yaml — the gate has stopped looking at anything")
	}

	owner := map[string]string{} // unit -> flow that first claimed it
	var doubleClaimed []string
	for _, flow := range slices.Sorted(maps.Keys(doc.Flows)) {
		for _, unit := range doc.Flows[flow] {
			if prev, dup := owner[unit]; dup {
				doubleClaimed = append(doubleClaimed, fmt.Sprintf("%s (in both %s and %s)", unit, prev, flow))
				continue
			}
			owner[unit] = flow
		}
	}
	if len(doubleClaimed) > 0 {
		slices.Sort(doubleClaimed)
		t.Errorf("units double-claimed across flows: %s", strings.Join(doubleClaimed, ", "))
	}

	var missingFromYAML []string
	for unit := range units {
		if _, ok := owner[unit]; !ok {
			missingFromYAML = append(missingFromYAML, unit)
		}
	}
	slices.Sort(missingFromYAML)
	if len(missingFromYAML) > 0 {
		t.Errorf("units carry rows in findings-index.md but no flow claims them: %s", strings.Join(missingFromYAML, ", "))
	}

	var orphanedInYAML []string
	for unit := range owner {
		if !units[unit] {
			orphanedInYAML = append(orphanedInYAML, unit)
		}
	}
	slices.Sort(orphanedInYAML)
	if len(orphanedInYAML) > 0 {
		t.Errorf("flow-partition.yaml names units that carry zero rows in findings-index.md: %s", strings.Join(orphanedInYAML, ", "))
	}
}

// severityStatusRowRe matches a severity-table row in its adjudication form:
// severity | count | resolved | open | partial | refuted | escalated.
// The "(unparsed)" label is the document's spelling of the "Unparsed severity"
// section heading.
var severityStatusRowRe = regexp.MustCompile(
	`^\|\s*(HIGH|MED|LOW|\(unparsed\))\s*\|\s*([\d,]+)\s*\|\s*([\d,]+)\s*\|\s*([\d,]+)\s*\|\s*([\d,]+)\s*\|\s*([\d,]+)\s*\|\s*([\d,]+)\s*\|`)

// TestArch_FindingsIndex_SeverityTableAdjudicationColumnsMatchTheRows checks
// every status column of the severity table against a per-section recount.
//
// It exists because "open" used to mean two different things in this one file.
// The status table and the Totals line use it for the STATUS — a row no commit
// names. The severity table's fourth column was written as count-minus-resolved,
// which silently folds PARTIAL, REFUTED and ESCALATED back in, and annotated the
// difference in prose ("39 (+6 refuted, +3 escalated, +9 partial)") that no test
// read. Worse, the "(unparsed)" row used the same notation additively, so the
// two readings disagreed by 18 rows inside a single table.
//
// Every status now gets its own gated column and "open" means the status
// everywhere in the document. Prose annotations carry no counts.
func TestArch_FindingsIndex_SeverityTableAdjudicationColumnsMatchTheRows(t *testing.T) {
	body := readIndex(t)
	got, _ := parseIndex(t, body)

	// The table labels the unparsed-severity section "(unparsed)".
	sectionFor := map[string]string{"(unparsed)": "Unparsed severity"}

	found := 0
	for _, line := range strings.Split(body, "\n") {
		m := severityStatusRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		found++
		label := m[1]
		sec, ok := sectionFor[label]
		if !ok {
			sec = label
		}
		num := func(s string) int {
			n, err := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
			if err != nil {
				t.Fatalf("unparseable count %q in the %s severity row", s, label)
			}
			return n
		}
		byStatus := got.byStatusBySeverity[sec]

		if n := num(m[2]); n != got.bySeverity[sec] {
			t.Errorf("severity table claims %s count=%d, the rows say %d", label, n, got.bySeverity[sec])
		}
		for _, col := range []struct {
			cell   string
			status string
		}{
			{m[3], "RESOLVED"},
			{m[4], "open"},
			{m[5], "PARTIAL"},
			{m[6], "REFUTED"},
			{m[7], "ESCALATED"},
		} {
			if n := num(col.cell); n != byStatus[col.status] {
				t.Errorf("severity table claims %s %s=%d, the rows say %d", label, col.status, n, byStatus[col.status])
			}
		}
		// A severity's columns must account for every row in its section,
		// or some status has appeared that this table does not model.
		sum := num(m[3]) + num(m[4]) + num(m[5]) + num(m[6]) + num(m[7])
		if sum != got.bySeverity[sec] {
			t.Errorf("severity %s: the status columns sum to %d but the section holds %d rows — an unmodelled status exists", label, sum, got.bySeverity[sec])
		}
	}
	if found != 4 {
		t.Fatalf("expected 4 severity rows (HIGH/MED/LOW/(unparsed)), recognised %d — the table format changed", found)
	}
}

// categoryColRe pulls the fourth column (Category) out of a finding row.
// Both 6-column ("## HIGH/MED/LOW" sections) and 7-column ("Unparsed
// severity" section) tables carry Category in the same position: ID,
// Status, Loc, Category, ...
var categoryColRe = regexp.MustCompile(`^\|\s*U\d+-F\d+\s*\|[^|]*\|[^|]*\|\s*([^|]*?)\s*\|`)

// attributedCategory reduces a possibly-compound category cell ("DEAD /
// NOPAY", "DEAD + CORRECTNESS") to a single bucket by keeping only the FIRST
// term. This is a documented choice, not a fact recovered from the data: the
// review process lists the primary category first and any secondary term
// after the delimiter, so first-term attribution matches how the reviewer
// themselves ordered the pair. A row with no delimiter (including the bare
// "—" placeholder) is its own bucket.
func attributedCategory(raw string) string {
	if i := strings.IndexAny(raw, "/+"); i >= 0 {
		return strings.TrimSpace(raw[:i])
	}
	return raw
}

// categoryTally recomputes per-category row counts, attributing every
// compound cell to its first term per attributedCategory's documented rule.
func categoryTally(body string) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		m := categoryColRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		counts[attributedCategory(m[1])]++
	}
	return counts
}

// parseCategoryTable reads the "| category | count |" summary table's claimed
// counts, stopping at the first line after the header that is not a
// two-column "| NAME | NUMBER |" row (the separator row is skipped, not
// treated as a stop).
func parseCategoryTable(t *testing.T, body string) map[string]int {
	t.Helper()
	lines := strings.Split(body, "\n")
	header := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "| category | count |" {
			header = i
			break
		}
	}
	if header == -1 {
		t.Fatal("no '| category | count |' table header found — the gate has stopped looking at anything")
	}

	rowRe := regexp.MustCompile(`^\|\s*([^|]+?)\s*\|\s*([\d,]+)\s*\|\s*$`)
	claimed := map[string]int{}
	for _, line := range lines[header+1:] {
		if strings.HasPrefix(strings.TrimSpace(line), "|---") {
			continue
		}
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			break
		}
		n, err := strconv.Atoi(strings.ReplaceAll(m[2], ",", ""))
		if err != nil {
			continue
		}
		claimed[m[1]] = n
	}
	return claimed
}

// TestArch_FindingsIndex_CategoryTableMatchesTheRows checks the
// "| category | count |" summary table, which no existing gate reads at all.
// See attributedCategory for the documented first-term rule applied to
// compound cells before comparison.
func TestArch_FindingsIndex_CategoryTableMatchesTheRows(t *testing.T) {
	body := readIndex(t)
	got := categoryTally(body)
	claimed := parseCategoryTable(t, body)

	if len(claimed) == 0 {
		t.Fatal("no category rows recognised — the table format changed and this gate has stopped looking at anything")
	}
	for _, cat := range slices.Sorted(maps.Keys(claimed)) {
		if claimed[cat] != got[cat] {
			t.Errorf("category table claims %s=%d, the rows say %d", cat, claimed[cat], got[cat])
		}
	}
	for _, cat := range slices.Sorted(maps.Keys(got)) {
		if _, ok := claimed[cat]; !ok {
			t.Errorf("category %s has %d rows but no entry in the category table", cat, got[cat])
		}
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
