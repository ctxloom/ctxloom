//go:build arch

// THE TABLE DECIDES. paths.HarpMembers is the ONE classification every
// walker, reaper and purge derives from. The code that removes session
// members by name is exactly where a second, hand-written classification
// regrows — a `[]string{ephemeral, persist}` here, an exclusion switch there
// — and each copy is a list that goes stale the day a row moves.
//
// This gate reads the reap, reclaim, purge and clean code and asks two
// things of every session-member constant it names:
//
//  1. It IS a row. A `paths.*FileName` / `paths.*DirName` constant named in
//     this code whose value is not a HarpMembers row is either a retired
//     member (delete the reference) or a file that never lived under a
//     session dir at all (the sessions-root index, say — this code has no
//     business naming it).
//  2. It is not hand-listed. A composite literal whose elements are member
//     constants is a member SET written by hand — the table is the set;
//     derive it (paths.ClassifyMember, the Lifetime axis, a row predicate).
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// reaperScopeFiles is the code this gate reads: every file that removes,
// classifies or reports session members by name. A file renamed or moved
// fails here loudly (the walk refuses a missing file), which is the point of
// naming them: this is a checked binding, not prose.
var reaperScopeFiles = []string{
	"internal/core/sessions/reap.go",
	"internal/adapters/operations/session_reclaim.go",
	"internal/adapters/operations/session_purge.go",
	"internal/adapters/operations/harp_artifacts.go",
	"internal/adapters/cli/clean_sessions.go",
	"internal/adapters/cli/session_purge_cmd.go",
}

// memberConstName matches the naming convention of an on-disk leaf constant
// in paths: the suffix says whether the leaf is a file or a directory.
var memberConstName = regexp.MustCompile(`(FileName|DirName)$`)

// pathsStringConsts parses internal/core/paths and returns every top-level
// string constant by name, so a selector `paths.X` in the scanned code can
// be resolved to the value the table holds.
func pathsStringConsts(t *testing.T) map[string]string {
	t.Helper()
	root := moduleRoot(t)
	dir := filepath.Join(root, "internal", "core", "paths")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	consts := map[string]string{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						consts[name.Name] = strings.Trim(lit.Value, "`\"")
					}
				}
			}
		}
	}
	if len(consts) < 20 {
		t.Fatalf("resolved only %d string constants in paths — the parse is broken, not the package", len(consts))
	}
	return consts
}

// memberSelector reports the constant name when expr is `paths.<X>` with X
// named like an on-disk leaf.
func memberSelector(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || x.Name != "paths" || !memberConstName.MatchString(sel.Sel.Name) {
		return "", false
	}
	return sel.Sel.Name, true
}

// TestArch_ReaperMemberNamesAreTableRows is the table-vs-constants gate.
func TestArch_ReaperMemberNamesAreTableRows(t *testing.T) {
	root := moduleRoot(t)
	consts := pathsStringConsts(t)
	rows := map[string]string{}
	for _, m := range paths.HarpMembers {
		rows[m.Name] = m.Rel()
	}

	fset := token.NewFileSet()
	var sites int
	var findings []string
	for _, rel := range reaperScopeFiles {
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
		if err != nil {
			t.Errorf("%s: %v — the reaper's scope file is missing or unparsable; if it moved, move this entry with it", rel, err)
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				for _, elt := range node.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						elt = kv.Value
					}
					if name, ok := memberSelector(elt); ok {
						findings = append(findings, fset.Position(elt.Pos()).String()+
							": paths."+name+" sits in a composite literal — a hand-listed member set; derive it from paths.HarpMembers instead")
					}
				}
			case *ast.SelectorExpr:
				name, ok := memberSelector(node)
				if !ok {
					return true
				}
				sites++
				value, known := consts[name]
				if !known {
					findings = append(findings, fset.Position(node.Pos()).String()+
						": paths."+name+" is not a string constant of paths")
					return true
				}
				if _, isRow := rows[value]; !isRow {
					findings = append(findings, fset.Position(node.Pos()).String()+
						": paths."+name+" ("+value+") is not a paths.HarpMembers row — retired member, or a file that never lived under a session dir; delete the reference")
				}
			}
			return true
		})
	}
	if sites < 5 {
		t.Fatalf("found only %d member-constant sites across the reaper's scope — the scan is broken, not the code", sites)
	}
	sort.Strings(findings)
	for _, f := range findings {
		t.Error(f)
	}
}
