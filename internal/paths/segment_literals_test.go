package paths

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkgSourceFile parses one of this package's own .go files, located from the
// compiled-in path of this test rather than the process working directory. A
// source-scanning gate rooted at "." reads nothing once anything moves the cwd:
// it matches zero sites, reports no debt, and still exits 0 — evaporating
// instead of failing.
func pkgSourceFile(t *testing.T, name string) (*token.FileSet, *ast.File) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller could not resolve this test's own source path")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(thisFile), name), nil, parser.ParseComments)
	require.NoError(t, err)
	require.NotEmpty(t, f.Decls, "parsed %s but found no declarations — the scan is looking at the wrong file", name)
	return fset, f
}

// TestPathSegments_ComeFromNamedConstants enforces this package's reason to
// exist.
//
// internal/paths is the single declarative source of truth for ctxloom's
// on-disk layout: if a path segment appears as a bare string anywhere in the
// repo, that is a duplication of this package. A bare segment inside THIS
// package is the same duplication with the shortest possible reach — the name
// exists nowhere, so no other package can refer to it, and anyone needing that
// directory must retype the string and hope it matches.
//
// The check is on whole Join ARGUMENTS. A constant carrying a file extension
// (ConfigFileName+".yaml") is a different shape and deliberately not flagged:
// the segment itself is named, and the suffix distinguishes the file from the
// directory of the same name.
func TestPathSegments_ComeFromNamedConstants(t *testing.T) {
	fset, f := pkgSourceFile(t, "paths.go")

	var joins int
	var offenders []string

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Join" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "filepath" {
			return true
		}
		joins++
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			offenders = append(offenders, fset.Position(lit.Pos()).String()+": "+lit.Value)
		}
		return true
	})

	require.NotZero(t, joins,
		"found no filepath.Join calls in paths.go — the scan matched nothing and would pass vacuously")
	assert.Empty(t, offenders,
		"these path segments are unnamed string literals; every other segment in this package is a "+
			"constant, and an unnamed one cannot be referenced, reused or renamed from anywhere else")
}

// TestTriggerAndTrustObjectSegments_AreUnchanged is the public-seam half: the
// paths these functions produce are what actually exist on users' disks, so
// naming their segments must move no bytes.
//
// The snapshot store DID move, deliberately — cache/ to state/, because nothing
// rebuilds it — so both spellings are pinned here: the live one, and the legacy
// one the migration still has to be able to find. A legacy path that quietly
// drifts is a migration that stops finding anything, which reads as "no project
// had snapshots" rather than as a bug.
func TestTriggerAndTrustObjectSegments_AreUnchanged(t *testing.T) {
	assert.Equal(t,
		filepath.Join("/project/.ctxloom", "state", "trust", "objects"),
		TrustObjectsPath("/project/.ctxloom"),
		"the approved-content snapshot store lives at state/trust/objects")
	assert.Equal(t,
		filepath.Join("/project/.ctxloom", "cache", "trust", "objects"),
		LegacyTrustObjectsPath("/project/.ctxloom"),
		"the migration must keep looking where the store used to be")

	// TriggerCacheDir is home-rooted, so assert on the tail it composes rather
	// than on a home directory this test has no business fixing.
	got, err := TriggerCacheDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(AppDirName, "cache", "triggers"),
		filepath.Join(filepath.Base(filepath.Dir(filepath.Dir(got))), filepath.Base(filepath.Dir(got)), filepath.Base(got)),
		"the revive-trigger verdict cache must stay at ~/.ctxloom/cache/triggers")
}
