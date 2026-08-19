//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE ARCHITECTURAL RULES' ANTI-VACUITY FLOORS. DO NOT DELETE THIS FILE.
//
// ctxloom's architectural rules live in internal/archlint as go/analysis
// analyzers, run by `just lint-arch` and the pre-commit hook. This file is
// what CANNOT live there, and it is not leftover scaffolding.
//
// go/analysis runs an analyzer once per package, and facts travel only along
// import edges. A package can therefore never learn how many other packages
// exist, how many files the sweep read, or how many declarations it looked at.
// Every one of those is a CORPUS assertion, and a corpus assertion is the only
// thing standing between these rules and a green tick that means nothing.
//
// The failure this guards against has happened here before and is always the
// same shape: a walk that silently matches nothing, a skip rule that prunes
// too much, a build-tag set that hides a whole directory. The rule then
// reports zero issues and exits 0 having never looked at anything. Every
// finding is absent, so the gate looks its healthiest at the exact moment it
// has stopped working.
//
// Each floor below is set far under the true count, so ordinary growth and
// deletion never trip it and only a BROKEN SWEEP does. If one fails, the
// answer is never to lower the number: it is that the corpus is not being
// read.
//
// This file is deliberately self-contained — its own root-finding and its own
// walk — so that deleting any other file in this package cannot quietly take
// the floors with it.

// corpusRoot walks up from the working directory to the directory holding
// go.mod, so the floors do not care where the suite is run from.
func corpusRoot(t *testing.T) string {
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

// corpusSkippedDir prunes the trees no rule reads: VCS and tooling metadata,
// fixtures the go tool itself excludes, and vendored or installed trees.
//
// The dot-directory rule is also what keeps a NESTED WORKTREE — an agent's
// separate checkout of this repo under .claude/worktrees/ — out of the count.
// Walking into one re-reads every source file in the module, inflating every
// number here by a whole extra copy per live worktree and turning a broken
// sweep into one that still clears its floor.
func corpusSkippedDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
		name == "testdata" || name == "vendor" || name == "node_modules"
}

// corpusCounts is what one walk of the module measures.
type corpusCounts struct {
	packages    int
	prodFiles   int
	engineFiles int
	documented  int
	vocabulary  int
}

// engineScopes are the subtrees the lock- and ledger-discipline rules read.
// They are counted separately because a rule scoped to five packages can be
// broken by a bad prefix while the module-wide count stays healthy.
var engineScopes = []string{
	"internal/claude",
	"internal/codex",
	"internal/kiro",
	"internal/opencode",
	"internal/shared/agent",
}

// walkCorpus reads the module once and counts what the rules depend on seeing.
func walkCorpus(t *testing.T) corpusCounts {
	t.Helper()
	root := corpusRoot(t)
	fset := token.NewFileSet()
	var c corpusCounts
	dirs := map[string]bool{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && p != root && corpusSkippedDir(d.Name()):
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
		dirs[dir] = true
		c.prodFiles++
		for _, scope := range engineScopes {
			if dir == scope || strings.HasPrefix(dir, scope+"/") {
				c.engineFiles++
				break
			}
		}

		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if perr != nil {
			// Unparseable source is a build failure elsewhere; do not mask it,
			// but do not let it silently shrink the corpus either.
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		c.documented += countDocumented(f)
		c.vocabulary += countVocabularies(f)
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	c.packages = len(dirs)
	return c
}

// countDocumented counts the declarations carrying a doc comment, the corpus
// the doc-comment rule reads.
func countDocumented(f *ast.File) int {
	n := 0
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Doc != nil {
				n++
			}
		case *ast.GenDecl:
			if d.Doc != nil {
				n++
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Doc != nil {
						n++
					}
				case *ast.ValueSpec:
					if s.Doc != nil {
						n++
					}
				}
			}
		}
	}
	return n
}

// countVocabularies counts the typed string constants that make a defined
// string type a closed vocabulary, the corpus the vocabulary rule discovers.
func countVocabularies(f *ast.File) int {
	n := 0
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil {
				continue
			}
			if _, ok := vs.Type.(*ast.Ident); !ok {
				continue
			}
			for _, val := range vs.Values {
				if bl, ok := val.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					n++
				}
			}
		}
	}
	return n
}

// TestArch_CorpusFloors_TheSweepActuallyReadsTheModule fails when the source
// walk every architectural rule depends on has stopped seeing the module.
//
// A rule that reads nothing reports nothing. This is the only assertion in the
// suite that can tell that apart from a module with no violations, and no
// analyzer can make it.
func TestArch_CorpusFloors_TheSweepActuallyReadsTheModule(t *testing.T) {
	c := walkCorpus(t)

	for _, floor := range []struct {
		name  string
		got   int
		min   int
		rules string
	}{
		{"packages", c.packages, 50, "archtestsupport, archlayering"},
		{"production files", c.prodFiles, 200, "archpathauthority, archwritediscipline"},
		{"production files under the engine scopes", c.engineFiles, 20, "archlockdiscipline, archledgerdiscipline"},
		{"documented declarations", c.documented, 1000, "archdoccomment"},
		{"typed string constants", c.vocabulary, 30, "archvocabulary"},
	} {
		if floor.got < floor.min {
			t.Errorf("the module walk saw only %d %s (floor %d) — the sweep is broken, not the module. "+
				"The %s rule(s) read this corpus; below this count their silence proves nothing. "+
				"Do NOT lower the floor.", floor.got, floor.name, floor.min, floor.rules)
		}
	}
}

// TestArch_CorpusFloors_GeneratedFrameEncoderExists fails when the generated
// encoder the reminder-frame rule protects is absent.
//
// Without it no frames exist to be constructed anywhere, so the rule passes by
// finding nothing — which is the failure it exists to catch, wearing a green
// tick. The rule itself cannot check this: the file is in another package, and
// an analyzer that is never handed that package never runs at all.
func TestArch_CorpusFloors_GeneratedFrameEncoderExists(t *testing.T) {
	const encoder = "internal/agentcoord/xmllike_gen.go"
	if _, err := os.Stat(filepath.Join(corpusRoot(t), encoder)); err != nil {
		t.Fatalf("%s is missing: run `just gen-mcp-schemas`. Without it the reminder-frame rule "+
			"proves nothing, because no frames exist to be constructed anywhere: %v", encoder, err)
	}
}
