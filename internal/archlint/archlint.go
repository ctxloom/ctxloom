// Package archlint holds ctxloom's architectural rules as go/analysis
// analyzers, so they run in the lint channel — editors, `just lint-arch`, the
// pre-commit hook — rather than only inside a tag-gated test binary.
//
// SCOPE OF THIS PACKAGE, AND WHAT DELIBERATELY STAYS A TEST.
//
// go/analysis runs an analyzer once per package, and facts travel only along
// import edges. Two properties follow, and both bound what may live here:
//
//   - A rule whose subject is one package's own syntax converts exactly. Most
//     of ctxloom's architectural rules are that shape: an import must not
//     exist, a literal must not appear outside its owner, a call must be
//     wrapped.
//   - A rule whose subject is the CORPUS — "the sweep saw at least N files" —
//     cannot be expressed at all, because a package can never learn how many
//     other packages exist. Those assertions are the anti-vacuity floors that
//     make the gates trustworthy, and they remain in tests/arch as
//     TestArch_CorpusFloors_*. Never move them here; there is nowhere to put
//     them.
//
// Rules that assert over RUNTIME VALUES (roster membership, resolver output,
// host-home byte equality) also stay in tests/arch: an analyzer sees syntax
// and types, never values.
package archlint

import (
	"go/ast"
	"path"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// ModulePath is this module's import path. Import strings are resolved against
// it to recover the module-relative directory a rule is written in terms of.
const ModulePath = "github.com/ctxloom/ctxloom"

// Analyzers returns every architectural analyzer, in a stable order.
func Analyzers() []*analysis.Analyzer {
	return []*analysis.Analyzer{
		TestSupportAnalyzer,
		LayeringAnalyzer,
		DocCommentAnalyzer,
		PathAuthorityAnalyzer,
		WriteDisciplineAnalyzer,
		LockDisciplineAnalyzer,
		LedgerDisciplineAnalyzer,
		SessionBindAnalyzer,
		ReminderFrameAnalyzer,
		VocabularyAnalyzer,
	}
}

// PkgDir is the module-relative directory of the package under analysis
// ("internal/operations"), or "" for a package outside this module.
//
// Derived from the package path rather than from a file path so it is
// unaffected by where the driver was invoked from, and so a linked worktree
// (a second checkout of this repo under another path) cannot change the
// answer.
func PkgDir(pass *analysis.Pass) string {
	return LocalDir(pass.Pkg.Path())
}

// SkipPass reports whether this pass is a duplicate or synthetic view of a
// package that some other pass already covers properly.
//
// The driver hands each package to an analyzer up to three times: the real
// package, its TEST VARIANT (the same production files plus the in-package
// _test.go files), and a synthesized "<pkg>.test" main whose only file is a
// generated stub in the build cache. Two consequences, and both have bitten:
//
//   - The synthetic .test main has no production source at all. A rule that
//     reads its files finds nothing, which for an allowlist-liveness check
//     looks exactly like "every exemption has gone stale" — a whole ratchet
//     reported as rotten because the pass was never looking at the package.
//   - The test variant contributes the same production files a second time,
//     so every real violation would be reported twice.
//
// Analyzing only the plain package makes each production file the subject of
// exactly one pass.
func SkipPass(pass *analysis.Pass) bool {
	if strings.HasSuffix(pass.Pkg.Path(), ".test") {
		return true
	}
	for _, f := range pass.Files {
		if IsTestFile(pass, f) {
			return true
		}
	}
	return false
}

// LocalDir turns a module-local import path into the directory it resolves to,
// or "" for a stdlib or third-party import.
func LocalDir(importPath string) string {
	if importPath == ModulePath {
		return "."
	}
	if !strings.HasPrefix(importPath, ModulePath+"/") {
		return ""
	}
	return path.Clean(strings.TrimPrefix(importPath, ModulePath+"/"))
}

// UnderSubtree reports whether dir is subtree itself or lies beneath it. Rules
// are written against subtrees, so "internal/cli" covers "internal/cli/tui"
// while never matching a sibling like "internal/clifmt".
func UnderSubtree(dir, subtree string) bool {
	return dir == subtree || strings.HasPrefix(dir, subtree+"/")
}

// IsTestFile reports whether the file at this position is a _test.go file.
//
// Architectural rules govern PRODUCTION code: a test may import an engine
// package for a fixture, or name a path literal in an assertion, without the
// architecture having drifted. The driver hands an analyzer the test variant
// of a package alongside the real one, so every rule here filters explicitly
// rather than relying on which variant it was handed.
func IsTestFile(pass *analysis.Pass, f *ast.File) bool {
	return strings.HasSuffix(pass.Fset.Position(f.Pos()).Filename, "_test.go")
}

// ProdFiles returns the non-test files of the package under analysis.
func ProdFiles(pass *analysis.Pass) []*ast.File {
	var out []*ast.File
	for _, f := range pass.Files {
		if !IsTestFile(pass, f) {
			out = append(out, f)
		}
	}
	return out
}

// FileRel is the module-relative path of a parsed file, for messages that name
// a file rather than a position.
func FileRel(pass *analysis.Pass, f *ast.File) string {
	name := pass.Fset.Position(f.Pos()).Filename
	dir := PkgDir(pass)
	if dir == "" {
		return name
	}
	return dir + "/" + path.Base(name)
}

// ImportPaths returns the distinct import paths of the package's production
// files, paired with the position to report a violation at.
func ImportPaths(pass *analysis.Pass) map[string]*ast.ImportSpec {
	out := map[string]*ast.ImportSpec{}
	for _, f := range ProdFiles(pass) {
		for _, spec := range f.Imports {
			p, err := ImportPathOf(spec)
			if err != nil || out[p] != nil {
				continue
			}
			out[p] = spec
		}
	}
	return out
}

// ImportPathOf decodes an import spec's quoted path.
func ImportPathOf(spec *ast.ImportSpec) (string, error) {
	return strconv.Unquote(spec.Path.Value)
}
