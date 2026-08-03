//go:build arch

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A DOC COMMENT MUST NOT RESTATE ITS OWN OPENING.
//
// Go's convention is one "// Name ..." opening per declaration. A SECOND
// sentence in the same doc that begins "Name " and then continues with the
// same words is not a stylistic quirk — it is a doc comment that was pasted
// twice, and the two copies do not stay equal. What lands is a stale first
// copy describing behaviour the code no longer has, sitting immediately above
// the current one, with nothing marking which is which.
//
// This is not hypothetical and it is not cosmetic. Three examples from the
// sweep that produced this gate:
//
//   - scripts/gendocs/livingdocs' stepIsAssertion carried a first copy
//     asserting the PRE-fix behaviour and a second explaining why that was
//     backwards (found and fixed earlier);
//   - internal/remote/repo_cache.go's safeRepoPath had a stale copy promising
//     a "fall back to baseDir" that the current implementation deliberately
//     REMOVED, because falling back to baseDir is what let RemoveAll wipe the
//     entire clone cache;
//   - internal/operations/hooks.go's maybeRegenerateContext had a stale copy
//     documenting a one-value return for a function that returns two.
//
// A reader who stops at the first paragraph — which is what a doc comment's
// first paragraph is FOR, and what `go doc` shows — reads the wrong one.
//
// The rule is deliberately narrow: it fires only when a later sentence opens
// with the declaration's own name AND continues identically for another
// restatedRunes runes. A doc that legitimately mentions its subject again in a
// later paragraph ("Close returns only once the pump goroutine has returned")
// diverges immediately and is not flagged.
func TestArch_DocCommentsDoNotRestateTheirOwnOpening(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	var findings []string
	var scanned int

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && p != root && skippedDir(d.Name()):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(d.Name(), ".go"):
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if perr != nil {
			// Unparseable source is a build failure elsewhere; do not mask it,
			// but do not let it silently shrink the sweep either.
			t.Errorf("parse %s: %v", p, perr)
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		n, found := scanFileDocs(f, filepath.ToSlash(rel))
		scanned += n
		findings = append(findings, found...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// A sweep that found nothing to look AT proves nothing; guard the gate
	// against silently becoming vacuous (a changed skip rule, a walk that
	// stopped early).
	if scanned < 1000 {
		t.Fatalf("only %d documented declarations scanned — the sweep is too small to be believed", scanned)
	}

	sort.Strings(findings)
	for _, f := range findings {
		t.Errorf("doc comment restates its own opening: %s\n"+
			"    one of the two copies is stale. Delete it — keeping both leaves a reader "+
			"who stops at the first paragraph reading the wrong one.", f)
	}
}

// skippedDir reports the directory names the sweep does not descend into —
// the same exclusions scan() uses. The dot-dir rule is also what keeps
// .claude/worktrees (another agent's checkout of this repo) out of this
// module's gate.
func skippedDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
		name == "testdata" || name == "vendor" || name == "node_modules"
}

// scanFileDocs checks every documented declaration in one parsed file,
// returning how many it looked at and one finding line per violation.
func scanFileDocs(f *ast.File, rel string) (scanned int, findings []string) {
	for _, decl := range f.Decls {
		for _, dd := range declDocs(decl) {
			scanned++
			if at := restatedOpening(dd.name, dd.doc); at != "" {
				findings = append(findings, rel+": "+dd.name+" — "+at)
			}
		}
	}
	return scanned, findings
}

// restatedRunes is how far a second "Name ..." sentence must continue
// IDENTICALLY before it counts as a restatement rather than a later reference
// to the same symbol. Thirty runes past the name is far beyond coincidence for
// English prose and well short of the ~60-100 the real cases share.
const restatedRunes = 30

// documented pairs a declaration's name with its doc comment text.
type documented struct {
	name string
	doc  string
}

// declDocs returns the (name, doc) pairs a declaration contributes. A grouped
// `var (...)` / `const (...)` block contributes one per spec that carries its
// OWN doc, plus the block's doc when the block declares a single spec — which
// is how a lone `var x = ...` with a doc above it is written.
func declDocs(decl ast.Decl) []documented {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Doc == nil || d.Name == nil {
			return nil
		}
		return []documented{{d.Name.Name, d.Doc.Text()}}
	case *ast.GenDecl:
		return genDeclDocs(d)
	}
	return nil
}

// genDeclDocs is declDocs' type/var/const arm. A spec with no doc of its own
// inherits the BLOCK's doc only when the block declares exactly one spec —
// which is how a lone `var x = ...` with a comment above it is written; in a
// grouped block the block doc describes the group, not any one member.
func genDeclDocs(d *ast.GenDecl) []documented {
	var out []documented
	for _, spec := range d.Specs {
		name, doc := specNameDoc(spec)
		if name == "" {
			continue
		}
		if doc == "" && len(d.Specs) == 1 && d.Doc != nil {
			doc = d.Doc.Text()
		}
		if doc != "" {
			out = append(out, documented{name, doc})
		}
	}
	return out
}

// specNameDoc pulls one spec's declared name and its own doc, if any.
func specNameDoc(spec ast.Spec) (name, doc string) {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Name != nil {
			name = s.Name.Name
		}
		if s.Doc != nil {
			doc = s.Doc.Text()
		}
	case *ast.ValueSpec:
		if len(s.Names) > 0 {
			name = s.Names[0].Name
		}
		if s.Doc != nil {
			doc = s.Doc.Text()
		}
	}
	return name, doc
}

// restatedOpening reports the shared text when doc opens with "name ..." more
// than once and two of those openings continue identically for at least
// restatedRunes runes. "" when the doc is fine.
//
// Whitespace is collapsed first, so the comparison is against the PROSE rather
// than against where the lines happen to wrap — a pasted copy that was
// re-wrapped is the same defect and must not escape on a line break.
func restatedOpening(name, doc string) string {
	prose := strings.Join(strings.Fields(doc), " ")
	starts := openingIndexes(prose, name)
	for a := 0; a < len(starts); a++ {
		for b := a + 1; b < len(starts); b++ {
			shared := commonPrefix(prose[starts[a]:], prose[starts[b]:])
			if len([]rune(shared)) >= len([]rune(name))+restatedRunes {
				return "restated as: " + truncate(shared, 90)
			}
		}
	}
	return ""
}

// openingIndexes finds every position in prose where name begins a sentence:
// the very start, or immediately after a sentence-ending ". ".
func openingIndexes(prose, name string) []int {
	var out []int
	for i := 0; i+len(name) < len(prose); i++ {
		if !strings.HasPrefix(prose[i:], name+" ") {
			continue
		}
		if i == 0 || (i >= 2 && prose[i-2] == '.' && prose[i-1] == ' ') {
			out = append(out, i)
		}
	}
	return out
}

// commonPrefix returns the longest common prefix of a and b, cut at a rune
// boundary so the reported text is never half a multi-byte character.
func commonPrefix(a, b string) string {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return string(ra[:n])
}

// truncate shortens s to at most n runes, marking the cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
