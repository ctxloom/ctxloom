//go:build arch

package arch

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// renameMapPath is the slice-0b rename map: one row per package the rename
// found, naming where it went. The table is the one binding between the
// design's ring directories and the tree that is CHECKED — by the two tests
// below — rather than read and believed.
const renameMapPath = "docs/architecture/audit-2026-09-18/31-rename-map.md"

// ringPrefixes are the directories a ctxloom package may live in after the
// rename (Part 0 of the decided architecture): the three rings, the toolbox,
// the family products, the test-only tree and the composition roots. The
// module also carries packages that are not ctxloom's to restructure — the
// standalone `pkg/clifmt` library, the embedded `resources` and `container`
// data, the `scripts/` tools and the `tests/` trees — and those roots are
// tolerated by name.
var ringPrefixes = []string{
	"internal/core",
	"internal/adapters",
	"internal/engines",
	"internal/shared",
	"internal/ltk",
	"internal/taskloom",
	"internal/testsupport",
	"cmd",
	"pkg",
	"resources",
	"container",
	"scripts",
	"tests",
}

// renameRow is one line of the rename map's table.
type renameRow struct {
	today, target, ring, source string
}

// readRenameMap parses the map's markdown table. It fails rather than
// returning an empty slice: a map that parsed to nothing would make both
// tests below pass vacuously.
func readRenameMap(t *testing.T) []renameRow {
	t.Helper()
	f, err := os.Open(filepath.Join(moduleRoot(t), renameMapPath))
	if err != nil {
		t.Fatalf("open the rename map: %v", err)
	}
	defer f.Close()
	var rows []renameRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 4 {
			t.Fatalf("rename map row has %d cells, want 4: %q", len(cells), line)
		}
		for i := range cells {
			cells[i] = strings.Trim(strings.TrimSpace(cells[i]), "`")
		}
		rows = append(rows, renameRow{today: cells[0], target: cells[1], ring: cells[2], source: cells[3]})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 100 {
		t.Fatalf("the rename map parsed to %d rows — the table is missing or its shape changed, not the module", len(rows))
	}
	return rows
}

// listModulePackages runs `go list ./...` and returns the module-relative
// import paths — every package, test-only ones included, which the source
// scan in arch_test.go deliberately does not see.
func listModulePackages(t *testing.T) map[string]bool {
	t.Helper()
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	pkgs := map[string]bool{}
	for _, ip := range strings.Fields(string(out)) {
		pkgs[strings.TrimPrefix(ip, modulePath+"/")] = true
	}
	if len(pkgs) < 100 {
		t.Fatalf("go list ./... reported %d packages — the listing is broken, not the module", len(pkgs))
	}
	return pkgs
}

// diesInPlace collects the map's left-column paths that the design retires
// rather than moves: they are the only packages permitted outside the rings.
func diesInPlace(rows []renameRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		if r.target == "dies in place" {
			out[r.today] = true
		}
	}
	return out
}

// TestArch_RenameMap_LeftColumnGone holds the rename map true in both
// directions: a moved package's old path is no longer a package and its new
// path is; a retired or staying package is still exactly where the map says.
// A row that goes false either way is a package the tree and the map
// disagree about, which is the drift this table exists to catch.
func TestArch_RenameMap_LeftColumnGone(t *testing.T) {
	rows := readRenameMap(t)
	pkgs := listModulePackages(t)

	for _, r := range rows {
		switch r.target {
		case "dies in place", "stays":
			if !pkgs[r.today] {
				t.Errorf("rename map says %s %s, but it is not a package in this module — delete the row or re-point it", r.today, r.target)
			}
		default:
			if pkgs[r.today] {
				t.Errorf("rename map moves %s to %s, but the old path is still a package — the move has not landed", r.today, r.target)
			}
			if !pkgs[r.target] {
				t.Errorf("rename map moves %s to %s, but the target is not a package in this module", r.today, r.target)
			}
		}
	}
}

// TestArch_Rings_EveryPackageInsideARing is the confinement gate Part 4.1 row
// 0b names: `go list ./...` shows no package outside the ring directories,
// the family products, the test-only tree and the composition roots. The only
// exceptions are the packages the rename map marks as dying in place — they
// are deleted, not moved, by the slice their row names — so a package outside
// the rings without such a row is either unmoved or newly born in the wrong
// place.
func TestArch_Rings_EveryPackageInsideARing(t *testing.T) {
	retired := diesInPlace(readRenameMap(t))
	pkgs := listModulePackages(t)

	var stray []string
	for p := range pkgs {
		if retired[p] || archrules.UnderAny(p, ringPrefixes) {
			continue
		}
		stray = append(stray, p)
	}
	sort.Strings(stray)
	for _, p := range stray {
		t.Errorf("package %s is outside every ring directory %v and is not a dies-in-place row of %s", p, ringPrefixes, renameMapPath)
	}
}
