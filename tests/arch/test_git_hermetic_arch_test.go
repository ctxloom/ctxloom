//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// hermeticGitFile and hermeticGitFunc name the one place test code may build a
// git process itself: taskstest.GitCmd.
const (
	hermeticGitFile = "internal/shared/tasks/taskstest/gitfixture.go"
	hermeticGitFunc = "GitCmd"
)

// TEST CODE RUNS GIT ONLY THROUGH taskstest.GitCmd.
//
// A test's git that inherits GIT_DIR, runs in an implicit directory, or walks
// up out of its temp root operates on the developer's REAL checkout, whose
// .git/config every worktree of it shares. That has happened: an integration
// suite run under `git bisect run` inherited the bisected worktree's GIT_DIR,
// and a fixture's `git init` + identity writes turned the main checkout into a
// bare repository carrying the test identity. GitCmd is the constructor that
// makes a test's git hermetic; a raw exec.Command("git", ...) in test code
// bypasses every one of its defences, so it is refused here.
//
// Test code is every _test.go file, every file under tests/ and
// internal/testsupport/, and every package named *test (taskstest, spooltest,
// …), which exist only to serve tests.
func TestArch_TestGitGoesThroughTheHermeticHelper(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var findings, envFindings []string
	sanctioned := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return skipModuleDir(root, p, d)
		case !strings.HasSuffix(d.Name(), ".go"):
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", p, perr)
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if !isTestCode(rel, f) {
			return nil
		}
		for _, pos := range unhermeticEnvOverrides(f) {
			envFindings = append(envFindings, rel+":"+strconv.Itoa(fset.Position(pos).Line))
		}
		for _, site := range rawGitExecs(f) {
			if rel == hermeticGitFile && site.fn == hermeticGitFunc {
				sanctioned++
				continue
			}
			findings = append(findings, rel+":"+strconv.Itoa(fset.Position(site.pos).Line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if sanctioned != 1 {
		t.Fatalf("expected exactly one git exec inside %s's %s, found %d — the detector or the helper "+
			"moved, and this gate is no longer checking what it claims", hermeticGitFile, hermeticGitFunc, sanctioned)
	}
	sort.Strings(findings)
	for _, f := range findings {
		t.Errorf("test code execs git directly: %s\n"+
			"    build the process with taskstest.GitCmd (or run it with taskstest.Git): it strips an "+
			"inherited GIT_DIR, refuses an implicit directory and fences discovery at the temp root.", f)
	}
	sort.Strings(envFindings)
	for _, f := range envFindings {
		t.Errorf("test code replaces a GitCmd process's environment with one that is not hermetic: %s\n"+
			"    wrap it: cmd.Env = taskstest.HermeticGitEnv(<env>). A replaced Env discards GitCmd's "+
			"own, and with it the GIT_DIR scrub and the discovery fence.", f)
	}
}

// isTestCode reports whether the file at module-relative path rel is test code.
func isTestCode(rel string, f *ast.File) bool {
	return strings.HasSuffix(rel, "_test.go") ||
		strings.HasPrefix(rel, "tests/") ||
		strings.HasPrefix(rel, "internal/testsupport/") ||
		strings.HasSuffix(f.Name.Name, "test")
}

type gitExecSite struct {
	pos token.Pos
	fn  string // enclosing top-level function, "" at package level
}

// rawGitExecs returns every exec.Command / exec.CommandContext call in f whose
// program is the literal "git", under whatever name f imports os/exec as.
func rawGitExecs(f *ast.File) []gitExecSite {
	name := ""
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == "os/exec" {
			name = "exec"
			if imp.Name != nil {
				name = imp.Name.Name
			}
		}
	}
	if name == "" || name == "_" {
		return nil
	}
	var out []gitExecSite
	for _, decl := range f.Decls {
		fn := ""
		if fd, ok := decl.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != name {
				return true
			}
			prog := -1
			switch sel.Sel.Name {
			case "Command":
				prog = 0
			case "CommandContext":
				prog = 1
			}
			if prog < 0 || len(call.Args) <= prog {
				return true
			}
			if lit, ok := call.Args[prog].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, _ := strconv.Unquote(lit.Value); v == "git" {
					out = append(out, gitExecSite{pos: call.Pos(), fn: fn})
				}
			}
			return true
		})
	}
	return out
}

// unhermeticEnvOverrides returns every `v.Env = X` in f where v holds a GitCmd
// process and X is neither HermeticGitEnv(...) nor an append onto v.Env itself.
func unhermeticEnvOverrides(f *ast.File) []token.Pos {
	var out []token.Pos
	for _, decl := range f.Decls {
		bound := map[string]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range as.Rhs {
				if i < len(as.Lhs) && isCallTo(rhs, "GitCmd") {
					if id, ok := as.Lhs[i].(*ast.Ident); ok {
						bound[id.Name] = true
					}
				}
			}
			return true
		})
		ast.Inspect(decl, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			sel, ok := as.Lhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Env" {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || !bound[id.Name] {
				return true
			}
			if !isCallTo(as.Rhs[0], "HermeticGitEnv") && !appendsOnto(as.Rhs[0], id.Name) {
				out = append(out, as.Pos())
			}
			return true
		})
	}
	return out
}

// isCallTo reports whether e calls a function named name, qualified or not.
func isCallTo(e ast.Expr, name string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == name
	case *ast.SelectorExpr:
		return fn.Sel.Name == name
	}
	return false
}

// appendsOnto reports whether e is append(v.Env, ...), which keeps GitCmd's
// hermetic environment as its base.
func appendsOnto(e ast.Expr, v string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || !isCallTo(e, "append") || len(call.Args) == 0 {
		return false
	}
	sel, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Env" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == v
}
