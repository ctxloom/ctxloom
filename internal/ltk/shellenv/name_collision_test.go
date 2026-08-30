package shellenv

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

const (
	ltkShellenv    = "github.com/ctxloom/ctxloom/internal/ltk/shellenv"
	sharedShellenv = "github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// Two packages in this module are called shellenv. They do different jobs:
// this one maps a shell executable NAME to a dialect ltk can parse;
// internal/shared/shellenv resolves the user's login shell PATH so ctxloom can
// find binaries a login shell would have put on PATH.
//
// The collision has a real cost the day a single file needs both, because one
// of them must then be aliased and every `shellenv.` at that call site stops
// naming a single thing. Today no file does — measured below, and this test is
// what keeps that true. If it ever fails, the answer is to RENAME one package,
// not to alias the import: an alias fixes the compile and leaves the reader
// exactly where the row said they would be.
//
// This is the smallest thing that actually holds the line. Renaming a package
// now, against a harm nothing has met, is a churn-for-hypothesis trade the
// measurement does not support.
func TestNoFileImportsBothShellenvPackages(t *testing.T) {
	root := moduleRoot(t)

	var checked, offenders int
	var names []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "website":
				return filepath.SkipDir
			}
			// A checkout hosting agent worktrees (.claude/worktrees/agent-*)
			// carries a full second copy of the module at a stale commit. Its
			// files are not this module's source, so a collision there would
			// fail this gate for whoever's checkout happened to be the busy
			// one. Never skip root: an agent worktree IS a linked worktree and
			// the suite routinely runs from one, so skipping the root would
			// scan nothing — the `checked` floor below is what catches that.
			if path != root && taskstest.IsLinkedWorktreeRoot(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // generated or otherwise unparseable; not this test's business
		}
		checked++
		// A file living INSIDE one of the two packages already has that one
		// in scope unqualified, so importing the other is the same collision
		// as importing both from a third package.
		dir := filepath.ToSlash(filepath.Dir(path))
		ltk := strings.HasSuffix(dir, "internal/ltk/shellenv")
		shared := strings.HasSuffix(dir, "internal/shared/shellenv")
		for _, imp := range f.Imports {
			switch strings.Trim(imp.Path.Value, `"`) {
			case ltkShellenv:
				ltk = true
			case sharedShellenv:
				shared = true
			}
		}
		if ltk && shared {
			offenders++
			rel, _ := filepath.Rel(root, path)
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// The scan must have actually happened. A source-walking gate that finds
	// nothing because it started in the wrong directory reports a clean sweep
	// while sweeping nothing, and no exit code distinguishes the two.
	if checked < 100 {
		t.Fatalf("only %d .go files scanned under %s — the walk did not reach the source tree", checked, root)
	}
	if offenders > 0 {
		t.Fatalf("these files import both shellenv packages, so one must be aliased and `shellenv.` "+
			"no longer names one thing — rename a package rather than aliasing: %v", names)
	}
}

// moduleRoot locates the module root from this file's COMPILED-IN path, not
// the working directory, for the reason the scan-size assertion above states.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's own source file")
	}
	for dir := filepath.Dir(self); ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(self))
		}
		dir = parent
	}
}
