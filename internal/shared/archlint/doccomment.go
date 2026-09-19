package archlint

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// restatedRunes is how far a second "Name ..." sentence must continue
// IDENTICALLY before it counts as a restatement rather than a later reference
// to the same symbol. Thirty runes past the name is far beyond coincidence for
// English prose and well short of what the real cases share.
const restatedRunes = 30

// DocCommentAnalyzer enforces that a doc comment does not restate its own
// opening.
//
// Go's convention is one "// Name ..." opening per declaration. A SECOND
// sentence in the same doc that begins with the name and continues with the
// same words is a doc comment that was pasted twice, and the two copies do not
// stay equal. What lands is a stale first copy describing behaviour the code
// no longer has, sitting immediately above the current one, with nothing
// marking which is which — and the first paragraph is exactly what `go doc`
// shows and what a reader stops at.
//
// The rule is deliberately narrow: it fires only when a later sentence opens
// with the declaration's own name AND continues identically for another
// restatedRunes runes. A doc that legitimately mentions its subject again in a
// later paragraph diverges immediately and is not flagged.
var DocCommentAnalyzer = &analysis.Analyzer{
	Name: "archdoccomment",
	Doc:  "a doc comment must not restate its own opening sentence",
	Run:  runDocComment,
}

func runDocComment(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	for _, f := range pass.Files {
		for _, decl := range f.Decls {
			for _, dd := range declDocs(decl) {
				at := restatedOpening(dd.name, dd.doc)
				if at == "" {
					continue
				}
				pass.Reportf(dd.pos,
					"doc comment for %s restates its own opening (%s) — one of the two copies is "+
						"stale. Delete it: keeping both leaves a reader who stops at the first "+
						"paragraph reading the wrong one.", dd.name, at)
			}
		}
	}
	return nil, nil
}

// documented pairs a declaration's name with its doc comment text.
type documented struct {
	name string
	doc  string
	pos  token.Pos
}

// declDocs returns the (name, doc) pairs a declaration contributes.
func declDocs(decl ast.Decl) []documented {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Doc == nil || d.Name == nil {
			return nil
		}
		return []documented{{d.Name.Name, d.Doc.Text(), d.Name.Pos()}}
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
		name, doc, pos := specNameDoc(spec)
		if name == "" {
			continue
		}
		if doc == "" && len(d.Specs) == 1 && d.Doc != nil {
			doc = d.Doc.Text()
		}
		if doc != "" {
			out = append(out, documented{name, doc, pos})
		}
	}
	return out
}

// specNameDoc pulls one spec's declared name and its own doc, if any.
func specNameDoc(spec ast.Spec) (name, doc string, pos token.Pos) {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Name != nil {
			name, pos = s.Name.Name, s.Name.Pos()
		}
		if s.Doc != nil {
			doc = s.Doc.Text()
		}
	case *ast.ValueSpec:
		if len(s.Names) > 0 {
			name, pos = s.Names[0].Name, s.Names[0].Pos()
		}
		if s.Doc != nil {
			doc = s.Doc.Text()
		}
	}
	return name, doc, pos
}

// restatedOpening reports the shared text when doc opens with "name ..." more
// than once and two of those openings continue identically for at least
// restatedRunes runes. "" when the doc is fine.
//
// Whitespace is collapsed first, so the comparison is against the PROSE rather
// than against where the lines happen to wrap: a pasted copy that was
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
