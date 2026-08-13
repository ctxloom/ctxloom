//go:build arch

// SESSION-STORE PATH SEGMENTS LIVE IN internal/paths.
//
// internal/paths is documented (docs/architecture/core/paths.md) as "the
// single declarative source of truth for ctxloom's on-disk layout"; its own
// contract statement is explicit: "if a path segment appears as a string
// literal anywhere else in the repo, that is a duplication of this package."
// A duplicate is worse than dead code — it is a SECOND spelling of a fact
// (".ctxloom", "sessions", "coord", …) that paths.Layout() cannot see and
// doctor cannot walk, exactly the failure mode the fs-consolidation plan's
// survey caught twice (D10): `coord.stateDirForProject` and
// `discover.List` both hand-roll "~/.ctxloom/coord" beside a legitimate
// `paths.AppDirName` reference in the same filepath.Join call, so the
// "coord" segment is invisible to Layout/doctor even though the ".ctxloom"
// segment right next to it is spelled correctly.
//
// That co-occurrence is this gate's detection signal, not a literal
// blacklist: a filepath.Join (or path.Join) call outside internal/paths that
// ALREADY references the paths package (proving it is building somewhere
// under the ctxloom-managed tree) and ALSO carries a bare string literal or a
// same-package CONST identifier as one of its other segments is exactly the
// shape of a hand-rolled duplicate — the paths reference shows the author
// reached for the vocabulary package and then, for this one segment, didn't.
// A Join call with no paths reference at all is out of scope on purpose
// (this gate is about ctxloom's OWN tree, not every path built anywhere in
// the module); a Join call that names every segment through paths.* (like
// memory.Compactor.saveEssence's filepath.Join(harpDir, paths.EssenceFileName))
// is compliant and never flagged, even when a MORE direct paths.Xxx helper
// exists to fold the two calls into one — that compositional style question
// is C9/C10's, not this gate's.
//
// CONST (not var) is the second half of the signal: a package-level const is
// Go's own way of saying "this is a fixed, compile-time name", which is
// precisely the shape of a path segment. An ordinary runtime-built path
// variable (harpDir, dir, essencePath, …) is never a const, so it is never
// flagged; the moment one WOULD need to be a const to hold a segment name
// (discover.DirName = "coord", coord's local coordDirName alias) is the
// moment it belongs in internal/paths instead.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// pathAuthorityExemptDir is the one directory this gate does not apply to —
// it is the vocabulary itself, not a consumer of it.
const pathAuthorityExemptDir = "internal/paths"

// segmentLiteralPattern is what "looks like a path segment" for this gate's
// purposes: letters, digits, dot, dash, underscore. It exists to keep glob
// patterns (filepath.Glob's "*" wildcard, e.g. in discover.List) and other
// non-segment string literals that happen to sit in a Join call's argument
// list out of the baseline — they are not a duplicated NAME, so flagging
// them would be noise the ratchet then has to carry forever.
var segmentLiteralPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// pathAuthorityAllowed is this gate's shrinking allowlist, in the same shape
// as writeDisciplineAllowed: a durable symbol reference ("file.go#Symbol")
// mapped to the fix required to remove the entry.
//
// Generated MECHANICALLY by running this gate with an empty map and
// transcribing every reported violation (2026-08-13, base bd6b3baf). Nothing
// here is migrated in this slice — that is C8/C9/C10's job.
var pathAuthorityAllowed = map[string]string{
	"internal/agentcoord/coord/statedir.go#stateDirForProject": "C8: move the \"coord\" segment (aliased here as coordDirName = discover.DirName) into internal/paths + Layout()",
	"internal/agentcoord/discover/discover.go#List":            "C8: move the \"coord\" (DirName) and \"endpoint.json\" (FileName) segments into internal/paths + Layout()",
	"internal/remote/publish.go#PublishPath":                   "duplicates the existing paths.BundlesDir (\"bundles\") constant instead of referencing it — one-line fix, not yet scheduled to a plan slice",
}

// pathAuthorityViolation is one Join call this gate found that mixes a
// paths.* reference with a segment the paths package does not name.
type pathAuthorityViolation struct {
	file     string
	symbol   string
	segments []string // the suspect argument(s), for the error message
	line     int
}

func (v pathAuthorityViolation) key() string {
	return v.file + "#" + v.symbol
}

// parsedFile is one non-test .go file this scan looked at, kept around
// (rather than re-parsed) between the const-collection pass and the
// call-scanning pass.
type parsedFile struct {
	rel string
	f   *ast.File
}

// scanPathAuthority walks the whole module outside pathAuthorityExemptDir,
// grouping files by directory (a Go package may span several files, and a
// const this gate cares about can be declared in one file and used in
// another — internal/agentcoord/discover's DirName/FileName both live in the
// same file here, but the mechanism does not assume that). For every
// directory it first collects the package-level CONST names declared there,
// then scans every filepath.Join/path.Join call for the co-occurrence
// pattern described in this file's header comment.
func scanPathAuthority(t *testing.T) []pathAuthorityViolation {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	byDir := map[string][]parsedFile{}
	var filesScanned int

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && skippedDir(d.Name()):
			return filepath.SkipDir
		case d.IsDir():
			return nil
		case !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go"):
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == pathAuthorityExemptDir || strings.HasPrefix(dir, pathAuthorityExemptDir+"/") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		filesScanned++
		byDir[dir] = append(byDir[dir], parsedFile{rel: rel, f: f})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if filesScanned < 200 {
		t.Fatalf("scanned only %d non-test files — the walk is broken, not the module", filesScanned)
	}

	var out []pathAuthorityViolation
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		consts := packageLevelConsts(byDir[dir])
		for _, pf := range byDir[dir] {
			out = append(out, scanFileForPathAuthority(fset, pf, consts)...)
		}
	}
	return out
}

// packageLevelConsts returns the set of package-level CONST names declared
// across every file in one directory's worth of parsed source.
func packageLevelConsts(files []parsedFile) map[string]bool {
	consts := map[string]bool{}
	for _, pf := range files {
		for _, decl := range pf.f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if n.Name != "_" {
						consts[n.Name] = true
					}
				}
			}
		}
	}
	return consts
}

// scanFileForPathAuthority finds every Join call in one file that mixes a
// paths.* reference with a segment paths does not name.
func scanFileForPathAuthority(fset *token.FileSet, pf parsedFile, consts map[string]bool) []pathAuthorityViolation {
	var out []pathAuthorityViolation
	for _, decl := range pf.f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		sym := funcSymbol(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isJoinCall(call) {
				return true
			}
			if !callReferencesPaths(call) {
				return true
			}
			var segments []string
			for _, arg := range call.Args {
				if seg, suspect := suspectSegment(arg, consts); suspect {
					segments = append(segments, seg)
				}
			}
			if len(segments) > 0 {
				out = append(out, pathAuthorityViolation{
					file: pf.rel, symbol: sym, segments: segments,
					line: fset.Position(call.Pos()).Line,
				})
			}
			return true
		})
	}
	return out
}

// isJoinCall reports whether call is filepath.Join or path.Join.
func isJoinCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && (pkg.Name == "filepath" || pkg.Name == "path")
}

// callReferencesPaths reports whether any argument of call, anywhere in its
// expression tree, selects off an identifier named "paths" — the signal that
// this Join call is building somewhere under the ctxloom-managed tree.
func callReferencesPaths(call *ast.CallExpr) bool {
	found := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			if found {
				return false
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "paths" {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			break
		}
	}
	return found
}

// suspectSegment reports whether arg is a bare string literal that looks
// like a path segment, or a bare identifier naming a package-level const —
// the two shapes a hand-rolled segment takes beside a paths.* reference —
// along with a description for the error message.
func suspectSegment(arg ast.Expr, consts map[string]bool) (string, bool) {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind != token.STRING {
			return "", false
		}
		val, err := strconv.Unquote(a.Value)
		if err != nil || val == "" || val == "." || val == ".." {
			return "", false
		}
		if !segmentLiteralPattern.MatchString(val) {
			return "", false // e.g. a glob wildcard like "*", not a named segment
		}
		return strconv.Quote(val), true
	case *ast.Ident:
		if consts[a.Name] {
			return a.Name + " (local const)", true
		}
	}
	return "", false
}

// TestArch_PathAuthority_SessionStoreLiteralsLiveInPaths is the gate: every
// filepath.Join/path.Join call outside internal/paths that references the
// paths package AND carries a bare literal or local-const segment must
// either not exist, or be named (with a reason) in pathAuthorityAllowed.
func TestArch_PathAuthority_SessionStoreLiteralsLiveInPaths(t *testing.T) {
	violations := scanPathAuthority(t)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	for _, v := range violations {
		if why, ok := pathAuthorityAllowed[v.key()]; ok {
			t.Logf("allowed: %s:%d %s segments=%v (%s)", v.file, v.line, v.symbol, v.segments, why)
			continue
		}
		t.Errorf("%s:%d (%s) builds a path alongside a paths.* reference using segment(s) %v that "+
			"internal/paths does not name — every ctxloom path segment must be a named constant there "+
			"(docs/architecture/core/paths.md). If this is a deliberate, reviewed exception, add %q to "+
			"pathAuthorityAllowed in tests/arch/path_authority_test.go naming the fix required to remove it.",
			v.file, v.line, v.symbol, v.segments, v.key())
	}
}

// TestArch_PathAuthority_AllowlistIsLive fails when a pathAuthorityAllowed
// entry names a symbol that either does not exist or no longer contains the
// violating co-occurrence — the same staleness check every other allowlist
// pair in this package runs. A stale exception would silently exempt
// whatever hand-rolled segment lands at that symbol next.
func TestArch_PathAuthority_AllowlistIsLive(t *testing.T) {
	violations := scanPathAuthority(t)
	live := make(map[string]bool, len(violations))
	for _, v := range violations {
		live[v.key()] = true
	}

	keys := make([]string, 0, len(pathAuthorityAllowed))
	for k := range pathAuthorityAllowed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if !live[k] {
			t.Errorf("pathAuthorityAllowed allows %q (%s) but the scan found no offending Join call "+
				"there anymore — delete the entry, or it will silently exempt whatever hand-rolled "+
				"segment lands at that symbol next", k, pathAuthorityAllowed[k])
		}
	}
}
