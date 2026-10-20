package archlint

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// stdStreamScopes are the subtrees this rule governs: the library code that
// runs inside a `ctxloom run` whose split view owns the terminal.
var stdStreamScopes = []string{"internal/adapters", "internal/core", "internal/engines", "internal/shared"}

// stdStreamExemptDirs own the terminal: the CLI decides what reaches the
// human's stdout and stderr, and when.
var stdStreamExemptDirs = []string{"internal/adapters/cli"}

// stdStreamFuncs are the package-level functions that print to a standard
// stream on their own: fmt's to stdout, log's through the default logger to
// stderr.
var stdStreamFuncs = map[string]map[string]bool{
	"fmt": {"Print": true, "Printf": true, "Println": true},
	"log": {
		"Print": true, "Printf": true, "Println": true,
		"Fatal": true, "Fatalf": true, "Fatalln": true,
		"Panic": true, "Panicf": true, "Panicln": true,
	},
}

// StdStreamsAnalyzer forbids library code from naming the process's own
// stdout or stderr: os.Stdout, os.Stderr, fmt.Print*, log's default-logger
// printers, and the print/println builtins.
//
// Under `ctxloom run` the split view owns the terminal. It points clidiag's
// sink at the session's diagnostics log, but a raw write to stderr bypasses
// that sink and is drawn over the view — a container's watch hint landed in
// the middle of the agent list that way. A diagnostic goes through clidiag
// (or a report.Sink handed in) instead; a deliberate exception names why it
// cannot reach that terminal in archrules.StdStreamsAllowed.
var StdStreamsAnalyzer = &analysis.Analyzer{
	Name: "archstdstreams",
	Doc:  "library code must not write to the process's own stdout/stderr; diagnostics go through clidiag",
	Run:  runStdStreams,
}

func runStdStreams(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) || pass.Pkg.Name() == "main" {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || !archrules.UnderAny(dir, stdStreamScopes) || archrules.UnderAny(dir, stdStreamExemptDirs) {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, f := range ProdFiles(pass) {
		rel := FileRel(pass, f)
		for _, decl := range f.Decls {
			sym := ""
			if fd, ok := decl.(*ast.FuncDecl); ok {
				sym = FuncSymbol(fd)
			}
			key := rel + "#" + sym
			ast.Inspect(decl, func(n ast.Node) bool {
				if what := stdStreamUse(pass.TypesInfo, n); what != "" {
					seen[key] = true
					reportStdStream(pass, n, rel, what, key)
				}
				return true
			})
		}
	}
	reportStaleAllowlist(pass, archrules.StdStreamsAllowed, analyzedFiles(pass), seen, "archrules.StdStreamsAllowed")
	return nil, nil
}

// reportStdStream reports the use n unless key is a reasoned exception.
func reportStdStream(pass *analysis.Pass, n ast.Node, rel, what, key string) {
	if _, ok := archrules.StdStreamsAllowed[key]; ok {
		return
	}
	pass.Reportf(n.Pos(),
		"%s writes to the process's own standard stream (%s) — under `ctxloom run` the split view "+
			"owns that terminal and the write is drawn over it, bypassing the diagnostics log "+
			"clidiag's sink points at. Report through clidiag (or a report.Sink you are handed). "+
			"If this cannot run while the split view is up, add %q to archrules.StdStreamsAllowed "+
			"naming why.",
		rel, what, key)
}

// stdStreamUse names the standard-stream use n is, or "" when it is none: a
// package-level os.Stdout/os.Stderr or printing function (never a method or a
// field that shares the name), or the print/println builtin.
func stdStreamUse(info *types.Info, n ast.Node) string {
	switch node := n.(type) {
	case *ast.SelectorExpr:
		obj := info.Uses[node.Sel]
		if obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
			return ""
		}
		pkg, name := obj.Pkg().Path(), obj.Name()
		if pkg == "os" && (name == "Stdout" || name == "Stderr") || stdStreamFuncs[pkg][name] {
			return pkg + "." + name
		}
	case *ast.Ident:
		if b, ok := info.Uses[node].(*types.Builtin); ok && (b.Name() == "print" || b.Name() == "println") {
			return b.Name()
		}
	}
	return ""
}
