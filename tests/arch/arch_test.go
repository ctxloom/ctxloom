//go:build arch

// Package arch holds the architectural gates that need the whole module at
// once — a corpus count, a prefix that must still match some package, a
// comparison between packages with no import edge between them — or that
// assert over runtime values, and which therefore cannot be go/analysis
// analyzers. The per-package rules live in
// internal/shared/archlint and run under `just lint-arch`.
//
// The module graph is built by parsing source rather than by shelling out to
// `go list`, so a gate needs no toolchain invocation, no module download, and
// no build tags to be satisfied.
package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// modulePath is this module's import path; local imports are resolved against
// it to turn an import string back into a directory.
const modulePath = "github.com/ctxloom/ctxloom"

// pkg is one directory's worth of parsed, non-test Go source.
type pkg struct {
	// dir is the module-relative directory ("internal/core/config").
	dir string
	// imports are the import paths of every non-_test.go file in it.
	imports []string
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod, so the gate does not care where it is run from.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root (no go.mod above the working directory)")
		}
		dir = parent
	}
}

// skipModuleDir skips, below root, hidden and underscore directories,
// testdata, vendor and node_modules.
func skipModuleDir(root, p string, d fs.DirEntry) error {
	name := d.Name()
	if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
		name == "testdata" || name == "vendor" || name == "node_modules") {
		return filepath.SkipDir
	}
	return nil
}

// isNonTestGoFile reports whether name is a non-test Go source file.
func isNonTestGoFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// skippedDir reports the directory names a module sweep does not descend
// into: VCS and tooling metadata, fixtures the go tool itself excludes, and
// vendored or installed trees. The dot-dir rule is also what keeps
// .claude/worktrees (another agent's checkout of this repo) out of this
// module's gates.
func skippedDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
		name == "testdata" || name == "vendor" || name == "node_modules"
}

// scan parses every non-test Go file in the module and returns the packages by
// module-relative directory. It fails the test rather than returning an error:
// a scan that quietly found nothing would make every assertion below vacuous.
func scan(t *testing.T) map[string]*pkg {
	t.Helper()
	root := moduleRoot(t)
	pkgs := map[string]*pkg{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && skippedDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		f, perr := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if perr != nil {
			// Unparseable source is a build failure elsewhere; do not mask it,
			// but do not let it silently shrink the graph either.
			t.Errorf("parse %s: %v", rel+"/"+name, perr)
			return nil
		}
		e, ok := pkgs[rel]
		if !ok {
			e = &pkg{dir: rel}
			pkgs[rel] = e
		}
		for _, spec := range f.Imports {
			ip, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				continue
			}
			if !slices.Contains(e.imports, ip) {
				e.imports = append(e.imports, ip)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}

	// Anti-vacuity: these assertions are only worth anything if the scan
	// actually saw the module. A walk that matched nothing would otherwise
	// pass every gate below forever.
	if len(pkgs) < 50 {
		t.Fatalf("scanned only %d packages — the source walk is broken, not the module", len(pkgs))
	}
	return pkgs
}
