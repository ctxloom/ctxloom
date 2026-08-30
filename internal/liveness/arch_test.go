//go:build arch

package liveness_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// ---------------------------------------------------------------------------
// THE CLASS GATE: an input record's field must be FILLED by somebody and READ
// by somebody. A field with only one of the two is a promise, not a mechanism.
//
// The defect this closes: liveness.Target is the whole input to
// the delegation watchdog — "everything the caller knows about one agent" —
// and four of its twelve fields were fiction. `Ended` and `ContainerID` were
// declared and read by nothing; `PID` and `WorkDir` were read by three of the
// five evidence sources and populated by nobody, so HostProbe, processCPU,
// cpuBurn and NewestMTime were unreachable in production and cpuRung's
// verdict was permanently its "no CPU evidence obtainable" default. Each
// field's doc comment described a capability the wiring did not have, and
// nothing anywhere disagreed.
//
// The rule, for every type in inputRecords: each exported field must have at
// least one non-test WRITER and at least one non-test READER somewhere in the
// module. Deleting the field is always an acceptable way to go green — a
// capability that does not exist should not be advertised in a struct any
// more than in a tool schema.
//
// WHY THIS IS NOT A NAME SCAN. `rg 'Ended' internal/liveness` is how the
// finding was originally made, and it is exactly the check that goes green on
// the wrong evidence: `Harp`, `Agent`, `Runtime`, `Detail` and `State` are all
// field names shared with half a dozen unrelated types in this module, so a
// name scan reports a live reader for a dead field and leaves the finding open
// behind a passing gate. This gate instead resolves, per file, which
// IDENTIFIERS are bound to the record type (parameters, receivers, results,
// var declarations, `:=` from a composite literal, struct fields) and only
// counts selector expressions rooted at one of those.
//
// WHAT THE GATE CANNOT REACH, and why each fails loudly rather than silently:
//
//   - Types are resolved SYNTACTICALLY, from declarations that name the type
//     in the same file. A record reached through an interface value, a type
//     switch, generics, or reflection is invisible. The cost is a false RED
//     (the field looks unread), which is loud; a whole-module name match
//     would instead produce false GREENS, which is the failure mode above.
//   - The registry is DECLARED, not discovered. A new caller-populated input
//     record is not covered until someone adds it below. That is a known
//     hole, not a hidden one: the point of the list is that adding an entry
//     is cheap and the entry then holds forever.
//   - "Read" means the field is loaded, not that it is USED WELL. A field
//     read into a variable that is then discarded still counts. Whether a
//     read changes an answer is what the ladder tests are for.
//   - Writers set through encoding/json, reflection, or a generated
//     constructor are invisible for the same syntactic reason.
//   - Test files are excluded on BOTH sides on purpose: a field written only
//     by a test is precisely that shape (the evidence source that only
//     ever runs under test), so counting test writers would green the exact
//     defect the gate exists to catch.
// ---------------------------------------------------------------------------

// inputRecord names one wired record: a struct whose two halves live in
// different places and can therefore drift apart silently. Both DIRECTIONS of
// that drift are the same defect and the same check —
//
//   - an INPUT record is filled by a caller and read by the package (a field
//     nobody fills makes every rule that reads it unreachable);
//   - an OUTPUT record is filled by the package and read by a caller (a field
//     nobody reads makes the work that produces it pointless, and — worse —
//     lets a distinction the producer went to the trouble of recording be
//     dropped by every consumer: FSObserved, gathered on every
//     tick and consulted by nothing, so "the walk found nothing" and "the
//     walk was denied" reached the verdict as the same fact).
type inputRecord struct {
	// pkgDir is the declaring package, module-relative.
	pkgDir string
	// typeName is the struct type.
	typeName string
	// qualifier is how importers of pkgDir name it in a selector
	// (`liveness.Target`).
	qualifier string
	// role is "input" or "output" — it changes nothing about the check, only
	// which side of a failure the message points at.
	role string
}

var inputRecords = []inputRecord{
	{pkgDir: "internal/liveness", typeName: "Target", qualifier: "liveness", role: "input"},
	{pkgDir: "internal/liveness", typeName: "Evidence", qualifier: "liveness", role: "output"},
}

func TestArch_InputRecords_EveryFieldIsBothWrittenAndRead(t *testing.T) {
	root := moduleRoot(t)
	files := moduleGoFiles(t, root)

	for _, rec := range inputRecords {
		t.Run(rec.pkgDir+"."+rec.typeName, func(t *testing.T) {
			fields := recordFields(t, root, rec)
			require.NotEmpty(t, fields, "no exported fields found — the registry entry is stale")

			written, read := map[string]bool{}, map[string]bool{}
			for _, f := range files {
				scanRecordUse(t, f, rec, written, read)
			}

			var deadWrite, deadRead []string
			for _, f := range fields {
				if !written[f] {
					deadWrite = append(deadWrite, f)
				}
				if !read[f] {
					deadRead = append(deadRead, f)
				}
			}
			sort.Strings(deadWrite)
			sort.Strings(deadRead)
			assert.Empty(t, deadWrite,
				"%s.%s (%s record): no production code POPULATES these fields, so every rule that reads them is unreachable — wire them or delete them",
				rec.qualifier, rec.typeName, rec.role)
			assert.Empty(t, deadRead,
				"%s.%s (%s record): no production code READS these fields, so the evidence they carry never reaches a decision — consume them or delete them",
				rec.qualifier, rec.typeName, rec.role)
		})
	}
}

// moduleRoot walks up from the test's working directory to the go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod above %s", dir)
		dir = parent
	}
}

// moduleGoFiles is every non-test, non-generated Go file in the module.
func moduleGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "node_modules":
				return filepath.SkipDir
			}
			// A checkout hosting agent worktrees (.claude/worktrees/agent-*)
			// carries a full second copy of the module at a stale commit;
			// scanning it means asserting against code that is not the code
			// under test. Never skip root: an agent worktree IS a linked
			// worktree and the suite routinely runs from one, so skipping the
			// root would gather nothing and pass vacuously.
			if path != root && taskstest.IsLinkedWorktreeRoot(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	}))
	// A FLOOR, not NotEmpty: the nested-worktree skip above is one bad
	// predicate away from pruning the whole walk, and "at least one file"
	// cannot tell a full module from the handful left after a collapse.
	require.GreaterOrEqual(t, len(out), moduleGoFileFloor,
		"only %d non-test .go files under %s — the walk collapsed (a bad skip, or a wrong root), "+
			"and every assertion built on it would be vacuous", len(out), root)
	return out
}

// moduleGoFileFloor sits far below the module's actual non-test .go file
// count, so ordinary churn — even deleting a whole package — cannot trip it,
// while a walk that pruned itself down to a handful still does. It is a
// collapse detector, not a size measurement; raising it to track the real
// count would only buy false failures.
const moduleGoFileFloor = 500

// recordFields lists the exported field names of the registered struct.
func recordFields(t *testing.T, root string, rec inputRecord) []string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rec.pkgDir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var fields []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != rec.typeName {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				for _, name := range fld.Names {
					if name.IsExported() {
						fields = append(fields, name.Name)
					}
				}
			}
			return false
		})
	}
	sort.Strings(fields)
	return fields
}

// scanRecordUse folds one file's writes and reads of the record's fields.
func scanRecordUse(t *testing.T, path string, rec inputRecord, written, read map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, path)

	// A file refers to the type either bare (it IS the declaring package) or
	// through whatever local name it imported the package under.
	local, inPkg := recordTypeName(f, rec)
	if local == "" && !inPkg {
		return
	}
	matches := func(e ast.Expr) bool { return typeExprIs(e, local, inPkg, rec.typeName) }

	bound := boundIdents(f, matches)
	// assignTargets are the selector nodes that appear as the LHS of a plain
	// assignment. Without this, `ev.FSObserved = ok` counts as a READ as well
	// as a write — ast.Inspect descends into the LHS and the generic selector
	// case fires on it — so a field the producer sets and nobody consults
	// satisfies both halves of the gate by itself. That is precisely the
	// wrong-evidence green this gate exists to refuse (it hid FSObserved's
	// own defect). Traversal is pre-order, so the AssignStmt is always seen
	// before its own LHS. Compound assignment (`x.N += 1`) and `x.N++` are
	// deliberately NOT recorded: those genuinely read the old value.
	assignTargets := map[ast.Node]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CompositeLit:
			if !matches(v.Type) {
				return true
			}
			for _, el := range v.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok {
						written[k.Name] = true
					}
				}
			}
		case *ast.AssignStmt:
			pure := v.Tok == token.ASSIGN || v.Tok == token.DEFINE
			for _, lhs := range v.Lhs {
				if name, ok := boundFieldSel(lhs, bound); ok {
					written[name] = true
					if pure {
						assignTargets[lhs] = true
					}
				}
			}
		case *ast.SelectorExpr:
			if assignTargets[n] {
				return true
			}
			if name, ok := boundFieldSel(v, bound); ok {
				read[name] = true
			}
		}
		return true
	})
}

// recordTypeName reports how this file names the record type: the local import
// alias for its package, or inPkg when the file IS the declaring package.
func recordTypeName(f *ast.File, rec inputRecord) (local string, inPkg bool) {
	if f.Name.Name == filepath.Base(rec.pkgDir) {
		// Same package name AND the file must actually be in it; the caller
		// only passes files, so confirm by looking for the type declaration
		// or by the import list below. A same-named package elsewhere in the
		// module would be a genuine ambiguity, and there is none today.
		inPkg = true
	}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasSuffix(p, "/"+rec.pkgDir) {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name, inPkg
		}
		return filepath.Base(rec.pkgDir), inPkg
	}
	return "", inPkg
}

// typeExprIs reports whether a type expression denotes the record type, in
// either `T`/`*T` (in-package) or `pkg.T`/`*pkg.T` (imported) form.
func typeExprIs(e ast.Expr, local string, inPkg bool, typeName string) bool {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	switch v := e.(type) {
	case *ast.Ident:
		return inPkg && v.Name == typeName
	case *ast.SelectorExpr:
		x, ok := v.X.(*ast.Ident)
		return ok && local != "" && x.Name == local && v.Sel.Name == typeName
	}
	return false
}

// boundIdents collects the identifiers in f whose declared type is the record
// type: parameters, receivers, results, struct fields, `var` specs, and `:=`
// from a composite literal of the type. This is the substitute for a real type
// checker, and its limits are documented on the gate above.
func boundIdents(f *ast.File, matches func(ast.Expr) bool) map[string]bool {
	bound := map[string]bool{}
	add := func(names []*ast.Ident) {
		for _, n := range names {
			bound[n.Name] = true
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Field:
			if matches(v.Type) {
				add(v.Names)
			}
		case *ast.ValueSpec:
			if matches(v.Type) {
				add(v.Names)
			}
		case *ast.AssignStmt:
			if v.Tok != token.DEFINE || len(v.Lhs) != len(v.Rhs) {
				return true
			}
			for i, rhs := range v.Rhs {
				cl, ok := rhs.(*ast.CompositeLit)
				if !ok || !matches(cl.Type) {
					continue
				}
				if id, ok := v.Lhs[i].(*ast.Ident); ok {
					bound[id.Name] = true
				}
			}
		case *ast.RangeStmt:
			// `for _, t := range []Target{...}` and friends.
			if id, ok := v.Value.(*ast.Ident); ok {
				if cl, ok := v.X.(*ast.CompositeLit); ok {
					if arr, ok := cl.Type.(*ast.ArrayType); ok && matches(arr.Elt) {
						bound[id.Name] = true
					}
				}
			}
		}
		return true
	})
	return bound
}

// boundFieldSel reports the field name when e is `<bound ident>.Field`.
func boundFieldSel(e ast.Expr, bound map[string]bool) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || !bound[id.Name] {
		return "", false
	}
	return sel.Sel.Name, true
}
