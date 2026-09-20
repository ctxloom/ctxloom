//go:build arch

package arch

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE DECIDED ARCHITECTURE'S DELETION LEDGER, slice 1a (docs/architecture/
// audit-2026-09-18/30-decided-architecture.md, Part 3.1 and Part 4.1 row 1a):
// the symbols, files and wire fields the pure-deletion slice retires. Each
// is asserted ABSENT so that it cannot quietly regrow — a deleted helper
// re-added "because it was handy" is the regression this gate exists for.
//
// The walk parses every .go file in the named package directory, test files
// included: a test that still declares or reaches a retired symbol is the
// same regrowth as production code doing it.

// retiredDecl is one top-level declaration that must not exist: an
// identifier declared in pkgDir, optionally scoped to a receiver type
// (a method) or a struct (a field).
type retiredDecl struct {
	pkgDir string
	recv   string // receiver type for a method, struct type for a field, "" for a plain decl
	name   string
	field  bool // name is a struct field of recv, not a method
}

// retiredSlice1aDecls is the ledger: item numbers follow the brief for row 1a.
var retiredSlice1aDecls = []retiredDecl{
	// 1. the dead half of operations/delegate.go — PreparedAgentChat.Start and
	// everything only it reached.
	{pkgDir: "internal/adapters/operations", recv: "PreparedAgentChat", name: "Start"},
	{pkgDir: "internal/adapters/operations", recv: "PreparedAgentChat", name: "startOneshot"},
	{pkgDir: "internal/adapters/operations", name: "AgentChatLaunch"},
	{pkgDir: "internal/adapters/operations", name: "dialChat"},
	{pkgDir: "internal/adapters/operations", name: "leadContextIn"},
	{pkgDir: "internal/adapters/operations", name: "chatDialResult"},
	{pkgDir: "internal/adapters/operations", name: "resolveChatDialTimeout"},
	{pkgDir: "internal/adapters/operations", name: "defaultChatDialTimeout"},
	{pkgDir: "internal/adapters/operations", recv: "AgentChatRequest", name: "ChatDialTimeout", field: true},
	{pkgDir: "internal/adapters/operations", recv: "PreparedAgentChat", name: "chatDialTimeout", field: true},
	{pkgDir: "internal/adapters/operations", recv: "PreparedAgentChat", name: "factory", field: true},
	// 2. claude.SessionConfigDir — an unused duplicate of the descriptor's
	// HomeVar.Subdir resolution.
	{pkgDir: "internal/engines/claude", name: "SessionConfigDir"},
	// 3. coord/publish.go — the in-process PublishEvents fallback nothing calls.
	{pkgDir: "internal/core/coord", recv: "Coordinator", name: "PublishEvents"},
	{pkgDir: "internal/core/coord", name: "rejectEvent"},
	// 4. ResolveAndHeal's liveness — three identical arms behind one enum.
	{pkgDir: "internal/adapters/operations", name: "Liveness"},
	{pkgDir: "internal/adapters/operations", name: "LivenessFinished"},
	{pkgDir: "internal/adapters/operations", name: "LivenessLive"},
	{pkgDir: "internal/adapters/operations", name: "LivenessUnknown"},
	// 5. the puller's lockfile `tree` — the per-pin shape flag.
	{pkgDir: "internal/adapters/remote", recv: "LockEntry", name: "Tree", field: true},
	// 6. the three permanent migrations (audit F14) — a one-shot upgrade
	// living inside a primitive or writer and so running forever.
	{pkgDir: "internal/core/agent", name: "cleanupLegacySidecar"},
	{pkgDir: "internal/adapters/confpatch", recv: "Store", name: "renameLegacyRecords"},
	// (the third, WriteCommandFiles' RemoveAll, is a call inside a surviving
	// function — asserted by TestArch_Retired_Slice1a_WriteCommandFilesDoesNotSweep)
	// 7. sessions.MigrateIndex + index_upgrade.go.
	{pkgDir: "internal/core/sessions", name: "MigrateIndex"},
	{pkgDir: "internal/core/sessions", name: "MigrationReport"},
	{pkgDir: "internal/core/sessions", name: "indexUpgrades"},
	{pkgDir: "internal/core/sessions", name: "tsNormalizeUpgrade"},
}

// retiredSlice1aFiles are whole files the slice deletes.
var retiredSlice1aFiles = []string{
	"internal/core/coord/publish.go",
	"internal/core/sessions/index_upgrade.go",
}

// reservedStartRunFields are the StartRun wire fields nothing reads on either
// side of the runner channel today (audit 11-dataflow-review U14). Each is
// asserted reserved by number and absent by name. The four fields Launch
// superseded (harness, input, parent_run_id, role) are written and read by
// nobody since slice 8 but stay on the message under the additive rule; they
// join this table in slice 9, when the plugin arm's twin reader dies.
var reservedStartRunFields = map[string]int{
	"task_id": 1,
	"budget":  5,
}

const coordinationProtoPath = "internal/adapters/coordgrpc/pb/coordination.proto"

// parsePackageDir parses every .go file directly under rel (tests included)
// and returns the files keyed by module-relative path. An unreadable or
// unparseable directory fails the test: a walk that found nothing would let
// every absence assertion pass vacuously.
func parsePackageDir(t *testing.T, rel string) map[string]*ast.File {
	t.Helper()
	dir := filepath.Join(moduleRoot(t), rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s/%s: %v", rel, e.Name(), perr)
		}
		files[filepath.ToSlash(filepath.Join(rel, e.Name()))] = f
	}
	if len(files) == 0 {
		t.Fatalf("%s holds no .go files — the walk is broken, not the module", rel)
	}
	return files
}

// receiverName returns the bare type name of a method receiver, or "".
func receiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	expr := fd.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// declSites returns every file in files that declares d.
func declSites(files map[string]*ast.File, d retiredDecl) []string {
	var sites []string
	for rel, f := range files {
		for _, decl := range f.Decls {
			switch n := decl.(type) {
			case *ast.FuncDecl:
				if d.field || n.Name.Name != d.name || receiverName(n) != d.recv {
					continue
				}
				sites = append(sites, rel)
			case *ast.GenDecl:
				for _, spec := range n.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if d.field {
							if s.Name.Name != d.recv {
								continue
							}
							st, ok := s.Type.(*ast.StructType)
							if !ok {
								continue
							}
							for _, fld := range st.Fields.List {
								for _, id := range fld.Names {
									if id.Name == d.name {
										sites = append(sites, rel)
									}
								}
							}
							continue
						}
						if d.recv == "" && s.Name.Name == d.name {
							sites = append(sites, rel)
						}
					case *ast.ValueSpec:
						if d.field || d.recv != "" {
							continue
						}
						for _, id := range s.Names {
							if id.Name == d.name {
								sites = append(sites, rel)
							}
						}
					}
				}
			}
		}
	}
	return sites
}

func (d retiredDecl) String() string {
	switch {
	case d.field:
		return d.pkgDir + "#" + d.recv + "." + d.name + " (field)"
	case d.recv != "":
		return d.pkgDir + "#" + d.recv + "." + d.name
	default:
		return d.pkgDir + "#" + d.name
	}
}

// TestArch_Retired_Slice1a asserts every symbol and file slice 1a deletes is
// gone from the tree.
func TestArch_Retired_Slice1a(t *testing.T) {
	parsed := map[string]map[string]*ast.File{}
	for _, d := range retiredSlice1aDecls {
		files, ok := parsed[d.pkgDir]
		if !ok {
			files = parsePackageDir(t, d.pkgDir)
			parsed[d.pkgDir] = files
		}
		for _, site := range declSites(files, d) {
			t.Errorf("retired symbol %s is still declared in %s — slice 1a deleted it; do not regrow it", d, site)
		}
	}
	root := moduleRoot(t)
	for _, rel := range retiredSlice1aFiles {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("retired file %s still exists — slice 1a deleted it", rel)
		}
	}
}

// TestArch_Retired_Slice1a_WriteCommandFilesDoesNotSweep pins the third
// permanent migration: claude.WriteCommandFiles used to RemoveAll the
// commands directory on every write, a one-shot upgrade that never
// retired. The function survives; the sweep inside it must not.
func TestArch_Retired_Slice1a_WriteCommandFilesDoesNotSweep(t *testing.T) {
	files := parsePackageDir(t, "internal/engines/claude")
	var found bool
	for rel, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "WriteCommandFiles" || fd.Recv != nil {
				continue
			}
			found = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RemoveAll" {
					t.Errorf("%s: WriteCommandFiles still calls RemoveAll — the permanent commands-dir sweep was deleted in slice 1a", rel)
				}
				return true
			})
		}
	}
	if !found {
		t.Fatal("claude.WriteCommandFiles not found — this gate pins a call inside it, so the function moving means the gate must move too")
	}
}

// TestArch_Retired_Slice1a_StartRunReservesDeadFields asserts the
// coordination proto reserves each dead StartRun field by number and no
// longer declares it by name, so neither side of the runner channel can
// silently repopulate a field the other never reads.
func TestArch_Retired_Slice1a_StartRunReservesDeadFields(t *testing.T) {
	body := startRunMessageBody(t)
	reserved := map[int]bool{}
	reservedRe := regexp.MustCompile(`^\s*reserved\s+([0-9,\s]+);`)
	fieldRe := regexp.MustCompile(`^\s*(?:optional\s+|repeated\s+)?[\w.]+\s+(\w+)\s*=\s*(\d+)\s*;`)
	declared := map[string]int{}
	for _, line := range body {
		if m := reservedRe.FindStringSubmatch(line); m != nil {
			for _, n := range strings.Split(m[1], ",") {
				n = strings.TrimSpace(n)
				if n == "" {
					continue
				}
				var v int
				for _, c := range n {
					v = v*10 + int(c-'0')
				}
				reserved[v] = true
			}
			continue
		}
		if m := fieldRe.FindStringSubmatch(line); m != nil {
			var v int
			for _, c := range m[2] {
				v = v*10 + int(c-'0')
			}
			declared[m[1]] = v
		}
	}
	if len(declared) == 0 {
		t.Fatal("StartRun parsed to zero fields — the message shape changed, not the wire")
	}
	for name, num := range reservedStartRunFields {
		if !reserved[num] {
			t.Errorf("StartRun does not reserve field %d (%s) — slice 1a retired it; a reserved number is what stops a later field reusing it", num, name)
		}
		if got, ok := declared[name]; ok {
			t.Errorf("StartRun still declares %s = %d — nothing on either side reads it; reserve the number instead", name, got)
		}
	}
}

// startRunMessageBody returns the lines between `message StartRun {` and its
// closing brace.
func startRunMessageBody(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(moduleRoot(t), coordinationProtoPath))
	if err != nil {
		t.Fatalf("open %s: %v", coordinationProtoPath, err)
	}
	defer f.Close()
	var body []string
	inMsg := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case !inMsg && strings.HasPrefix(strings.TrimSpace(line), "message StartRun {"):
			inMsg = true
		case inMsg && strings.TrimSpace(line) == "}":
			return body
		case inMsg:
			body = append(body, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("message StartRun not found in %s", coordinationProtoPath)
	return nil
}
