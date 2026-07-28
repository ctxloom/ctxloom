//go:build arch

// Package arch holds the module-wide architectural gates — the ones whose
// subject is the whole import graph rather than any one package, and which
// therefore have nowhere else to live.
//
// The invariant enforced here is that test-only machinery stays out of shipped
// code. `internal/testsupport` (and its subpackages `dockergate` and `parity`)
// is an ordinary Go package: nothing in the language, the build, or the linter
// config stops a production file importing it. Today none does — but the day
// one did, `internal/testsupport/parity`'s reflection sweep and `testing`
// dependency would be linked into the released binaries, and the only signal
// would be a slightly larger `ctxloom`. That isolation was convention; this
// makes it executable, and it fails through the same channel as its siblings
// (`TestArch_*`, `just test-arch`) rather than as a depguard lint the way a
// linter rule would.
//
// Two gates, deliberately overlapping:
//
//   - the SHIPPED-BINARY gate walks the import closure of every `cmd/...` main
//     package. This is the consequence that actually matters and it admits no
//     exemptions — nothing test-only may be reachable from a released binary.
//   - the WHOLE-MODULE gate is stricter: no non-test file anywhere may import
//     it, so a violation is caught at the package that introduces it rather
//     than only once something links it into a binary. Genuine test-harness
//     packages that are themselves not shipped are listed in
//     `testSupportImporters`, and a third gate fails if an entry there goes
//     stale.
//
// The graph is built by parsing imports out of the source tree rather than by
// shelling out to `go list`, so the gate needs no toolchain invocation, no
// module download, and no build tags to be satisfied — it sees tag-gated files
// too, which for a "must not import" rule is the conservative direction.
package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// modulePath is this module's import path; local imports are resolved against
// it to turn an import string back into a directory.
const modulePath = "github.com/ctxloom/ctxloom"

// forbiddenPrefix is the package tree that must not be reachable from shipped
// code. The trailing element is matched exactly or as a parent, so
// `internal/testsupport`, `internal/testsupport/parity` and
// `internal/testsupport/dockergate` are all covered, while a hypothetical
// `internal/testsupportish` would not be.
const forbiddenPrefix = modulePath + "/internal/testsupport"

// testSupportImporters are the non-shipped test-harness packages permitted to
// import the forbidden tree from a non-`_test.go` file. Each entry is a
// module-relative directory. An entry is a deliberate, reviewable admission —
// and TestArch_TestSupportAllowlist_IsLive fails if one stops being true, so a
// stale exemption cannot silently cover whatever later takes its path.
var testSupportImporters = map[string]string{
	// The integration harness is compiled only into `-tags integration` test
	// binaries; it is a test fixture that happens to live in ordinary .go
	// files so several test packages can share it.
	"tests/integration/testenv": "shared harness for the -tags integration suite; never linked into a binary",
	// The acceptance suite (godog step registrations, `//go:build acceptance`)
	// is the same shape: ordinary .go files that only ever compile into a
	// `-tags acceptance` test binary, never a shipped one. steps_isolation_probe.go
	// imports dockergate (U159-F02) to mirror its skip/fail decision for the
	// container-axis probe.
	"tests/acceptance": "godog acceptance suite, compiled only under -tags acceptance; never linked into a binary",
}

// pkg is one directory's worth of parsed, non-test Go source.
type pkg struct {
	// dir is the module-relative directory ("internal/config").
	dir string
	// imports are the import paths of every non-_test.go file in it.
	imports []string
	// isMain records whether the directory declares `package main`.
	isMain bool
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
			// Skip VCS/tooling/fixture trees. testdata is excluded by the go
			// tool itself, so its contents are not part of any package.
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "vendor" || name == "node_modules") {
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
		if f.Name.Name == "main" {
			e.isMain = true
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
	if _, ok := pkgs["internal/testsupport"]; !ok {
		t.Fatal("the scan did not find internal/testsupport — the gate is looking at the wrong tree")
	}
	return pkgs
}

// isForbidden reports whether an import path is inside the test-only tree.
func isForbidden(importPath string) bool {
	return importPath == forbiddenPrefix || strings.HasPrefix(importPath, forbiddenPrefix+"/")
}

// localDir turns a module-local import path into the directory it resolves to,
// or "" for a third-party/stdlib import.
func localDir(importPath string) string {
	if importPath == modulePath {
		return "."
	}
	if !strings.HasPrefix(importPath, modulePath+"/") {
		return ""
	}
	return path.Clean(strings.TrimPrefix(importPath, modulePath+"/"))
}

// TestArch_ShippedBinaries_DoNotDependOnTestSupport is the consequence gate:
// no package reachable from any cmd/... main may import the test-only tree.
// It reports the full import chain, because "package X imports testsupport" is
// not actionable on its own when X is six edges down from the binary.
func TestArch_ShippedBinaries_DoNotDependOnTestSupport(t *testing.T) {
	pkgs := scan(t)

	var mains []string
	for dir, p := range pkgs {
		if p.isMain && (dir == "cmd" || strings.HasPrefix(dir, "cmd/")) {
			mains = append(mains, dir)
		}
	}
	sort.Strings(mains)
	if len(mains) == 0 {
		t.Fatal("found no main packages under cmd/ — the gate has stopped looking at anything")
	}

	for _, main := range mains {
		// Breadth-first over local imports, remembering how each package was
		// reached so a violation can be reported as a chain.
		from := map[string]string{main: ""}
		queue := []string{main}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			p, ok := pkgs[cur]
			if !ok {
				continue // generated-but-absent or otherwise unscanned
			}
			for _, ip := range p.imports {
				if isForbidden(ip) {
					t.Errorf("shipped binary %s reaches the test-only tree: package %s imports %s\n  chain: %s",
						main, cur, ip, strings.Join(chain(from, cur), " -> "))
					continue
				}
				dep := localDir(ip)
				if dep == "" {
					continue
				}
				if _, seen := from[dep]; seen {
					continue
				}
				from[dep] = cur
				queue = append(queue, dep)
			}
		}
	}
}

// chain rebuilds the import path from a main package down to pkg.
func chain(from map[string]string, pkg string) []string {
	var out []string
	for cur := pkg; cur != ""; cur = from[cur] {
		out = append(out, cur)
		if from[cur] == "" {
			break
		}
	}
	slices.Reverse(out)
	return out
}

// TestArch_NonTestPackages_DoNotImportTestSupport is the stricter gate: a
// non-_test.go file anywhere in the module may not import the test-only tree
// unless its package is an admitted test harness. Catching it here means the
// violation is named at the package that introduced it, not only once
// something links it into a binary.
func TestArch_NonTestPackages_DoNotImportTestSupport(t *testing.T) {
	pkgs := scan(t)

	dirs := make([]string, 0, len(pkgs))
	for dir := range pkgs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		if dir == "internal/testsupport" || strings.HasPrefix(dir, "internal/testsupport/") {
			continue // the tree may of course import itself
		}
		for _, ip := range pkgs[dir].imports {
			if !isForbidden(ip) {
				continue
			}
			if why, admitted := testSupportImporters[dir]; admitted {
				t.Logf("admitted: %s imports %s (%s)", dir, ip, why)
				continue
			}
			t.Errorf("package %s imports %s from a non-test file — test-only machinery must not be "+
				"reachable from ordinary code. Move the helper into a _test.go file, or (if this package "+
				"is itself a test harness that is never linked into a binary) add it to testSupportImporters "+
				"in tests/arch/arch_test.go with a reason.", dir, ip)
		}
	}
}

// TestArch_TestSupportAllowlist_IsLive fails when testSupportImporters names a
// package that no longer exists or no longer imports the test-only tree. A
// stale exemption is worse than none: it silently covers whatever later takes
// that directory's name.
func TestArch_TestSupportAllowlist_IsLive(t *testing.T) {
	pkgs := scan(t)

	dirs := make([]string, 0, len(testSupportImporters))
	for dir := range testSupportImporters {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		p, ok := pkgs[dir]
		if !ok {
			t.Errorf("testSupportImporters names %q, which is not a package in this module — delete the entry", dir)
			continue
		}
		if !slices.ContainsFunc(p.imports, isForbidden) {
			t.Errorf("testSupportImporters exempts %q but it no longer imports %s — delete the entry, "+
				"or it will silently exempt whatever import lands there next", dir, forbiddenPrefix)
		}
	}
}
