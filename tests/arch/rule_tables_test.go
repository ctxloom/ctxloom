//go:build arch

// The architectural rules run twice: as the TestArch_ gates in this package
// and as go/analysis analyzers in internal/shared/archlint (the pre-commit
// hook). Two runners is fine; two DECLARATIONS of what they enforce is not.
// A rule table copied into both places drifts — one side gains a row the
// other never sees — and the stale copy keeps enforcing while lying. These
// gates pin the ruling that each table is declared ONCE, in a package neither
// runner owns, and that both runners read that declaration.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ruleTableNames are the package-level vars that carry an architectural
// rule set. Each must be declared in exactly one package.
var ruleTableNames = []string{
	"LayeringRules",
	"LedgerDisciplineAllowed",
	"LockDisciplineAllowed",
	"WriteDisciplineAllowed",
}

// ruleTableReaders are the two runners; each must read every table from the
// one package that declares it.
var ruleTableReaders = []string{
	"internal/shared/archlint",
	"tests/arch",
}

// ruleTableTrees are the subtrees the declaration scan covers: where the
// runners live and where a copy would be declared.
var ruleTableTrees = []string{"internal", "tests"}

// ruleTableDecl is one package-level var declaration of a rule-table name,
// under either spelling (exported or not).
type ruleTableDecl struct {
	table string // the canonical (exported) name
	dir   string // module-relative package directory
}

// scanRuleTableDecls parses every Go file (tests included, under every build
// tag) beneath ruleTableTrees and reports each package-level var whose name
// is one of ruleTableNames, case-insensitively — a copy named
// layeringRules is a copy.
func scanRuleTableDecls(t *testing.T) []ruleTableDecl {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var out []ruleTableDecl
	for _, tree := range ruleTableTrees {
		err := filepath.WalkDir(filepath.Join(root, tree), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			dir := filepath.ToSlash(rel)
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					for _, name := range spec.(*ast.ValueSpec).Names {
						for _, table := range ruleTableNames {
							if strings.EqualFold(name.Name, table) {
								out = append(out, ruleTableDecl{table: table, dir: dir})
							}
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) == 0 {
		t.Fatalf("the scan found no declaration of any of %v under %v — the gate is looking at the wrong tree", ruleTableNames, ruleTableTrees)
	}
	return out
}

// declaringPackages maps each table name to the sorted, distinct packages
// that declare it.
func declaringPackages(decls []ruleTableDecl) map[string][]string {
	seen := map[string]map[string]bool{}
	for _, d := range decls {
		if seen[d.table] == nil {
			seen[d.table] = map[string]bool{}
		}
		seen[d.table][d.dir] = true
	}
	out := map[string][]string{}
	for table, dirs := range seen {
		for dir := range dirs {
			out[table] = append(out[table], dir)
		}
		sort.Strings(out[table])
	}
	return out
}

// TestArch_RuleTables_DeclaredOnce fails when any rule table is declared in
// more than one package, or in none. Two declarations is the drift this gate
// exists to forbid; zero means a table was renamed out from under the gate.
func TestArch_RuleTables_DeclaredOnce(t *testing.T) {
	byTable := declaringPackages(scanRuleTableDecls(t))
	for _, table := range ruleTableNames {
		dirs := byTable[table]
		switch len(dirs) {
		case 1:
		case 0:
			t.Errorf("%s is declared nowhere under %v — if it was renamed, re-point ruleTableNames", table, ruleTableTrees)
		default:
			t.Errorf("%s is declared in %d packages (%v) — a rule table is declared once, in a package both runners read",
				table, len(dirs), dirs)
		}
	}
}

// TestArch_RuleTables_BothRunnersReadTheOneDeclaration fails when either
// runner does not read a table from the package that declares it. Read here
// means a selector `<pkg>.<Table>` in the runner's own source, where <pkg> is
// the declaring package's name — so the analyzer and the test enforce the
// SAME set by construction, not by two copies that happen to agree today.
// The declaring package must also be neither runner: a runner that owned
// the table would make the other one its client, and the ruling was a leaf
// neither side owns.
func TestArch_RuleTables_BothRunnersReadTheOneDeclaration(t *testing.T) {
	root := moduleRoot(t)
	byTable := declaringPackages(scanRuleTableDecls(t))
	for _, table := range ruleTableNames {
		dirs := byTable[table]
		if len(dirs) != 1 {
			// TestArch_RuleTables_DeclaredOnce reports this shape; there is
			// no single declaration to check the runners against.
			t.Errorf("%s has %d declaring packages (%v); nothing to check the runners against", table, len(dirs), dirs)
			continue
		}
		owner := dirs[0]
		for _, reader := range ruleTableReaders {
			if owner == reader {
				t.Errorf("%s is declared in runner %s — it belongs in a leaf package neither runner owns", table, owner)
				continue
			}
			if !packageReadsSelector(t, filepath.Join(root, reader), filepath.Base(owner), table) {
				t.Errorf("%s does not read %s.%s (declared in %s) — both runners must enforce the one declaration",
					reader, filepath.Base(owner), table, owner)
			}
		}
	}
}

// packageReadsSelector reports whether any Go file in dir contains the
// selector expression pkg.name.
func packageReadsSelector(t *testing.T, dir, pkg, name string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || found {
				return !found
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == pkg && sel.Sel.Name == name {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
