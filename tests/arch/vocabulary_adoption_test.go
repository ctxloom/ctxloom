//go:build arch

// A CLOSED VOCABULARY IS CONSUMED THROUGH ITS OWNER, NEVER RE-SPELLED.
//
// A closed vocabulary is a small set of user-typeable string values with one
// package that owns them: a defined string type plus its typed constants, and
// (usually) a parser that decides membership. The owner is normally correct.
// What fails is ADOPTION — a consumer somewhere reaches the vocabulary without
// going through the owner, and nothing in the language, the build, or the
// linter notices, because a string literal is not a call and `T(s)` compiles
// for any string s. The user-visible result is a typo that resolves to a
// silently different posture instead of an error.
//
// This gate is a DISCOVERY sweep, not a list of known vocabularies. It finds
// the vocabularies itself (see vocabularies below: any defined string type in
// internal/ or cmd/ with two or more typed string constants), so a vocabulary
// added tomorrow is governed the day it is declared. That property is the
// point: an enumerated gate whose coverage list can silently omit a member is
// the same defect one level up.
//
// Three rules, each a different way a consumer reaches past the owner:
//
//   - RAW CONVERSION (vocabConversionAllowed). `pkg.T(s)` outside T's own
//     package turns an arbitrary runtime string into a vocabulary value by
//     assertion. Whatever the owner offers to validate it — a parser, a
//     membership predicate — was not called, so an unrecognized value becomes
//     a well-typed value nobody rejected. A conversion of a string LITERAL
//     that is not one of T's declared members is the same fault with the bad
//     value written into the source.
//
//   - DUPLICATED MEMBERSHIP TEST (vocabMembershipAllowed). A function outside
//     the owner that string-compares against two or more of T's members is a
//     second copy of the membership test. It cannot follow T when a member is
//     added, renamed, or given an accepted alias — it does not import the
//     answer, it re-derives it.
//
//   - PARALLEL LIST (vocabParallelAllowed). Two functions in different
//     packages testing four or more of the same string literals are two copies
//     of one list even when nobody ever declared it as a type. This is the
//     undeclared-vocabulary case, where there is no owner to route through
//     yet — the finding is that a vocabulary exists and has no single home.
//
// Detection is purely syntactic (go/ast, no go/types), the same technique
// write_discipline_test.go and doc_comment_test.go use. Its blind spots are
// stated at each rule's discovery helper and summarized in the report the
// gate prints on failure.
//
// This gate is a RATCHET, like write_discipline_test.go: every site the scan
// found at authoring time is grandfathered into one of the three allowlists
// with a reason, and none is repaired here. What the gate buys immediately is
// that the set cannot grow silently, and
// TestArch_VocabularyAdoption_AllowlistsAreLive fails on any entry that has
// stopped being a violation, so the baseline can only shrink.
package arch

import (
	"fmt"
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

// vocabScopes are the subtrees this gate reads: production code only, both
// the library and the binaries that drive it. Test files never enter the scan.
var vocabScopes = []string{"internal", "cmd"}

// vocabMinMembers is how many typed string constants a defined string type
// needs before it counts as a closed vocabulary. Two is the smallest set where
// "which one is it" is a question a consumer can get wrong.
const vocabMinMembers = 2

// vocabMinSharedMembers is how many of a vocabulary's members one function
// outside the owner must test before it counts as a second copy of the
// membership test. One member is an ordinary comparison against a single
// value; two is a decision procedure over the vocabulary.
const vocabMinSharedMembers = 2

// vocabMinParallelLiterals is how many literals two functions in different
// packages must share before they count as parallel copies of one undeclared
// list. It is higher than vocabMinSharedMembers because there is no
// declaration anchoring the set: with nothing asserting these literals belong
// together, only a wide overlap distinguishes a shared vocabulary from two
// functions that happen to test some of the same words.
const vocabMinParallelLiterals = 4

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// vocabulary is one discovered closed vocabulary: a defined string type, the
// module-relative directory of the package that declares it, and the literal
// values of its typed constants.
type vocabulary struct {
	dir     string // "internal/lm/isolation"
	name    string // "WorkspaceAxis"
	members map[string]bool
}

func (v vocabulary) id() string { return v.dir + "." + v.name }

// vocabFile is one parsed production file plus the import-name-to-directory
// map needed to resolve a qualified identifier (`isolation.WorkspaceAxis`)
// back to the package that declares it.
type vocabFile struct {
	rel     string // module-relative path
	dir     string // module-relative directory
	file    *ast.File
	imports map[string]string // local package name -> module-relative dir
}

// vocabStringLit decodes e as a string literal. ok is false for anything that
// is not a string literal; an empty literal decodes with ok true, so callers
// that treat "" as a sentinel can say so explicitly rather than by accident.
func vocabStringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// vocabParseFiles parses every non-test .go file under vocabScopes. It fails
// the test rather than returning an error: a scan that quietly found nothing
// would make every assertion below vacuous.
func vocabParseFiles(t *testing.T, fset *token.FileSet) []vocabFile {
	t.Helper()
	root := moduleRoot(t)
	var out []vocabFile

	for _, scope := range vocabScopes {
		err := filepath.WalkDir(filepath.Join(root, scope), func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && skippedDir(d.Name()):
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

			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			imports := map[string]string{}
			for _, is := range f.Imports {
				ipath, uerr := strconv.Unquote(is.Path.Value)
				if uerr != nil {
					continue
				}
				dir := localDir(ipath)
				if dir == "" {
					continue
				}
				name := ipath[strings.LastIndex(ipath, "/")+1:]
				if is.Name != nil {
					name = is.Name.Name
				}
				imports[name] = dir
			}
			out = append(out, vocabFile{
				rel:     rel,
				dir:     filepath.ToSlash(filepath.Dir(rel)),
				file:    f,
				imports: imports,
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", scope, err)
		}
	}
	// Anti-vacuity: a walk that stopped matching files would make every
	// assertion below pass for the wrong reason.
	if len(out) < 400 {
		t.Fatalf("parsed only %d non-test files under %v — the walk is broken, not the tree", len(out), vocabScopes)
	}
	return out
}

// vocabDiscover finds every closed vocabulary in the parsed tree, plus the
// alias map that lets a re-exported name resolve to the declaring package.
//
// A vocabulary is a defined string type whose package declares at least
// vocabMinMembers typed constants with string-literal values. The type's own
// `type X string` declaration is not required to be visible to the scan: the
// typed const block is the load-bearing signal, since that is what makes the
// members a closed set rather than an open string.
//
// Aliases: `type A = pkg.B` re-exports another package's vocabulary under a
// local name, and a consumer writing `local.A(s)` is converting into pkg.B.
// The alias map redirects those to the declaring package so a re-export
// cannot launder a raw conversion.
//
// BLIND SPOT: a vocabulary whose members are not written as typed constants —
// a bare `map[string]T` table, a `[]string` of names, or a parser's switch
// cases with no consts beside them — is not discovered. Rule PARALLEL LIST is
// the partial answer for those, since a second copy of such a list still
// collides with the first.
func vocabDiscover(files []vocabFile) (map[string]*vocabulary, map[string]string) {
	vocabs := map[string]*vocabulary{}
	aliases := map[string]string{}

	for _, vf := range files {
		for _, decl := range vf.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gd.Tok {
			case token.TYPE:
				for _, sp := range gd.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok || !ts.Assign.IsValid() {
						continue // a definition, not an alias
					}
					sel, ok := ts.Type.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					id, ok := sel.X.(*ast.Ident)
					if !ok {
						continue
					}
					if owner, ok := vf.imports[id.Name]; ok {
						aliases[vf.dir+"."+ts.Name.Name] = owner + "." + sel.Sel.Name
					}
				}
			case token.CONST:
				// A grouped const block repeats its type only on the first
				// spec, so carry it forward the way the language does.
				var carried string
				for _, sp := range gd.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok {
						continue
					}
					if id, ok := vs.Type.(*ast.Ident); ok {
						carried = id.Name
					}
					if carried == "" {
						continue
					}
					for _, val := range vs.Values {
						lit, ok := vocabStringLit(val)
						if !ok {
							continue
						}
						key := vf.dir + "." + carried
						v := vocabs[key]
						if v == nil {
							v = &vocabulary{dir: vf.dir, name: carried, members: map[string]bool{}}
							vocabs[key] = v
						}
						v.members[lit] = true
					}
				}
			}
		}
	}

	for key, v := range vocabs {
		if len(v.members) < vocabMinMembers {
			delete(vocabs, key)
		}
	}
	return vocabs, aliases
}

// vocabTestFuncs are the strings.* helpers whose second argument is a value
// the first argument is being TESTED against. A literal in that position is
// part of a membership decision exactly as a `==` operand or a case value is.
var vocabTestFuncs = map[string]bool{
	"HasPrefix":  true,
	"HasSuffix":  true,
	"EqualFold":  true,
	"Contains":   true,
	"TrimPrefix": true,
	"TrimSuffix": true,
}

// membershipSite is one function and the set of string literals it tests a
// string against.
type membershipSite struct {
	dir  string
	file string
	sym  string
	lits map[string]int // literal -> first line it appears on
}

func (s membershipSite) key() string { return s.file + "#" + s.sym }

// vocabTestedLiterals collects the string literals a function body compares a
// string against: `==`/`!=` operands, switch case values, and the pattern
// argument of the vocabTestFuncs helpers. The empty literal is excluded — it
// is this codebase's "unset" sentinel everywhere and says nothing about which
// vocabulary is in play.
//
// BLIND SPOT: a literal reached any other way — a regexp, a table this
// function ranges over, a helper it calls with the literal as an argument —
// is not counted, so a consumer can still re-spell a vocabulary in a shape
// this scan does not read as a comparison.
func vocabTestedLiterals(fset *token.FileSet, body ast.Node) map[string]int {
	out := map[string]int{}
	add := func(e ast.Expr) {
		lit, ok := vocabStringLit(e)
		if !ok || lit == "" {
			return
		}
		if _, seen := out[lit]; !seen {
			out[lit] = fset.Position(e.Pos()).Line
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				add(x.X)
				add(x.Y)
			}
		case *ast.CaseClause:
			for _, e := range x.List {
				add(e)
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "strings" || !vocabTestFuncs[sel.Sel.Name] || len(x.Args) < 2 {
				return true
			}
			add(x.Args[1])
		}
		return true
	})
	return out
}

// vocabMembershipSites collects every function whose body tests a string
// against two or more distinct literals. One literal is an ordinary
// comparison; two or more is a decision procedure, which is the thing a
// vocabulary owner is supposed to be the only holder of.
func vocabMembershipSites(fset *token.FileSet, files []vocabFile) []membershipSite {
	var out []membershipSite
	for _, vf := range files {
		for _, decl := range vf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			lits := vocabTestedLiterals(fset, fd.Body)
			if len(lits) < 2 {
				continue
			}
			out = append(out, membershipSite{dir: vf.dir, file: vf.rel, sym: funcSymbol(fd), lits: lits})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// ---------------------------------------------------------------------------
// Rule: RAW CONVERSION
// ---------------------------------------------------------------------------

// conversionViolation is one `pkg.T(x)` conversion into a closed vocabulary
// made from outside T's own package.
type conversionViolation struct {
	file  string
	sym   string
	line  int
	vocab string // "internal/lm/isolation.WorkspaceAxis"
	lit   string // the offending literal, or "" for a runtime string
	isLit bool
}

func (c conversionViolation) key() string { return c.file + "#" + c.sym + "#" + c.vocab }

func (c conversionViolation) what() string {
	if c.isLit {
		return fmt.Sprintf("mints %s(%q), which is not one of its declared members", c.vocab, c.lit)
	}
	return fmt.Sprintf("converts a runtime string straight into %s", c.vocab)
}

// vocabScanConversions finds every conversion into a discovered vocabulary
// made outside the package that declares it.
//
// A conversion is recognized syntactically: a one-argument call whose callee
// is `pkg.T` where pkg resolves through the file's imports to a package
// declaring vocabulary T. Within a package a type name and a function name
// cannot collide, so `pkg.T(x)` with T a known type is a conversion and never
// a call.
//
// A conversion of a declared member literal is not a violation: the value is
// visible in the source and is in the set. The empty literal is likewise not
// a violation — it is the codebase's "unset, apply the default" sentinel.
//
// BLIND SPOT: with no type information the scan cannot tell
// `T(alreadyTypedValue)` (a no-op re-conversion) from `T(userInput)`, and
// counts both. It also cannot see a value that enters the vocabulary by
// assignment or struct literal rather than by conversion, which is how a
// vocabulary-typed struct field gets filled from a decoded config or wire
// message.
func vocabScanConversions(fset *token.FileSet, files []vocabFile, vocabs map[string]*vocabulary, aliases map[string]string) []conversionViolation {
	var out []conversionViolation
	for _, vf := range files {
		for _, decl := range vf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			sym := funcSymbol(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				ownerDir, ok := vf.imports[pkgIdent.Name]
				if !ok {
					return true
				}
				id := ownerDir + "." + sel.Sel.Name
				if target, ok := aliases[id]; ok {
					id = target
				}
				v, ok := vocabs[id]
				if !ok || v.dir == vf.dir {
					return true
				}
				lit, isLit := vocabStringLit(call.Args[0])
				if isLit && (lit == "" || v.members[lit]) {
					return true
				}
				out = append(out, conversionViolation{
					file: vf.rel, sym: sym, line: fset.Position(call.Pos()).Line,
					vocab: v.id(), lit: lit, isLit: isLit,
				})
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].line < out[j].line
	})
	return out
}

// ---------------------------------------------------------------------------
// Rule: DUPLICATED MEMBERSHIP TEST
// ---------------------------------------------------------------------------

// membershipViolation is one function outside a vocabulary's owner that tests
// vocabMinSharedMembers or more of that vocabulary's members.
type membershipViolation struct {
	file   string
	sym    string
	line   int
	vocab  string
	shared []string
}

func (m membershipViolation) key() string { return m.file + "#" + m.sym + "#" + m.vocab }

// vocabScanMembership finds functions that re-implement a vocabulary's
// membership test outside its owner.
//
// BLIND SPOT: a consumer that tests exactly ONE member (`if s == "worktree"`)
// is below the threshold and invisible. Raising the rule to one member is not
// viable — vocabulary members are ordinary English words ("default", "none",
// "plan") that appear as unrelated literals throughout the tree, and a gate
// that cries wolf on those is a gate that gets suppressed.
func vocabScanMembership(sites []membershipSite, vocabs map[string]*vocabulary) []membershipViolation {
	ids := make([]string, 0, len(vocabs))
	for id := range vocabs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []membershipViolation
	for _, site := range sites {
		for _, id := range ids {
			v := vocabs[id]
			if v.dir == site.dir {
				continue
			}
			var shared []string
			line := 0
			for lit, ln := range site.lits {
				if v.members[lit] {
					shared = append(shared, lit)
					if line == 0 || ln < line {
						line = ln
					}
				}
			}
			if len(shared) < vocabMinSharedMembers {
				continue
			}
			sort.Strings(shared)
			out = append(out, membershipViolation{file: site.file, sym: site.sym, line: line, vocab: id, shared: shared})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		if out[i].line != out[j].line {
			return out[i].line < out[j].line
		}
		return out[i].vocab < out[j].vocab
	})
	return out
}

// ---------------------------------------------------------------------------
// Rule: PARALLEL LIST
// ---------------------------------------------------------------------------

// parallelViolation is one pair of functions in different packages testing the
// same set of literals — an undeclared vocabulary with two homes.
type parallelViolation struct {
	a, b   string // "file#Symbol", a < b
	shared []string
}

func (p parallelViolation) key() string { return p.a + " ~ " + p.b }

// vocabScanParallel finds pairs of functions in different packages whose
// tested-literal sets overlap by vocabMinParallelLiterals or more.
//
// BLIND SPOT: the rule is pairwise and symmetric — it reports that two lists
// exist, not which one should own the vocabulary; that is a judgement for
// whoever fixes it. It also cannot see a list duplicated between a function
// and a data table, only between two functions.
func vocabScanParallel(sites []membershipSite) []parallelViolation {
	var out []parallelViolation
	for i := range sites {
		for j := i + 1; j < len(sites); j++ {
			if sites[i].dir == sites[j].dir {
				continue
			}
			var shared []string
			for lit := range sites[i].lits {
				if _, ok := sites[j].lits[lit]; ok {
					shared = append(shared, lit)
				}
			}
			if len(shared) < vocabMinParallelLiterals {
				continue
			}
			sort.Strings(shared)
			a, b := sites[i].key(), sites[j].key()
			if a > b {
				a, b = b, a
			}
			out = append(out, parallelViolation{a: a, b: b, shared: shared})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// ---------------------------------------------------------------------------
// The gates
// ---------------------------------------------------------------------------

// vocabScan runs the whole discovery once and returns the three violation
// sets, so the gates and the liveness check see identical input.
func vocabScan(t *testing.T) ([]conversionViolation, []membershipViolation, []parallelViolation) {
	t.Helper()
	fset := token.NewFileSet()
	files := vocabParseFiles(t, fset)
	vocabs, aliases := vocabDiscover(files)
	// Anti-vacuity: discovery that found no vocabularies would make all three
	// rules pass by finding nothing to check.
	if len(vocabs) < 30 {
		t.Fatalf("discovered only %d closed vocabularies — discovery is broken, not the tree", len(vocabs))
	}
	sites := vocabMembershipSites(fset, files)
	if len(sites) < 80 {
		t.Fatalf("found only %d functions testing two or more string literals — discovery is broken, not the tree", len(sites))
	}
	return vocabScanConversions(fset, files, vocabs, aliases),
		vocabScanMembership(sites, vocabs),
		vocabScanParallel(sites)
}

// TestArch_VocabularyAdoption_ConversionsGoThroughTheOwner is the RAW
// CONVERSION gate.
func TestArch_VocabularyAdoption_ConversionsGoThroughTheOwner(t *testing.T) {
	conversions, _, _ := vocabScan(t)
	for _, c := range conversions {
		if why, ok := vocabConversionAllowed[c.key()]; ok {
			t.Logf("allowed: %s:%d %s (%s)", c.file, c.line, c.what(), why)
			continue
		}
		t.Errorf("%s:%d in %s %s — a closed vocabulary is entered through the package that owns it "+
			"(its parser, or a conversion of one of its declared constants), never by asserting a "+
			"string into the type. If this is a deliberate, reviewed exception, add %q to "+
			"vocabConversionAllowed in tests/arch/vocabulary_adoption_test.go naming the fix required "+
			"to remove it.", c.file, c.line, c.sym, c.what(), c.key())
	}
}

// TestArch_VocabularyAdoption_MembershipTestsRouteThroughTheOwner is the
// DUPLICATED MEMBERSHIP TEST gate.
func TestArch_VocabularyAdoption_MembershipTestsRouteThroughTheOwner(t *testing.T) {
	_, membership, _ := vocabScan(t)
	for _, m := range membership {
		if why, ok := vocabMembershipAllowed[m.key()]; ok {
			t.Logf("allowed: %s:%d %s re-tests %s %v (%s)", m.file, m.line, m.sym, m.vocab, m.shared, why)
			continue
		}
		t.Errorf("%s:%d %s compares a string against %d members of %s %v — that is a second copy of "+
			"the membership test, which cannot follow the vocabulary when a member is added, renamed, "+
			"or aliased. Call the owner's parser or compare against its exported constants. If this is "+
			"a deliberate, reviewed exception, add %q to vocabMembershipAllowed in "+
			"tests/arch/vocabulary_adoption_test.go naming the fix required to remove it.",
			m.file, m.line, m.sym, len(m.shared), m.vocab, m.shared, m.key())
	}
}

// TestArch_VocabularyAdoption_NoParallelLists is the PARALLEL LIST gate.
func TestArch_VocabularyAdoption_NoParallelLists(t *testing.T) {
	_, _, parallel := vocabScan(t)
	for _, p := range parallel {
		if why, ok := vocabParallelAllowed[p.key()]; ok {
			t.Logf("allowed: %s %v (%s)", p.key(), p.shared, why)
			continue
		}
		t.Errorf("%s and %s each test the same %d literals %v — one vocabulary with two homes, and "+
			"nothing makes the second follow the first. Give it one owner and route both through it. "+
			"If this is a deliberate, reviewed exception, add %q to vocabParallelAllowed in "+
			"tests/arch/vocabulary_adoption_test.go naming the fix required to remove it.",
			p.a, p.b, len(p.shared), p.shared, p.key())
	}
}

// TestArch_VocabularyAdoption_AllowlistsAreLive fails when an allowlist entry
// names a site the scan no longer reports — the same staleness check
// TestArch_WriteDiscipline_AllowlistIsLive and
// TestArch_LayeringAllowlist_IsLive run for their own allowlists. A stale
// exception is worse than none: left in place it silently exempts whatever
// lands at that key next, and the baseline could never shrink.
func TestArch_VocabularyAdoption_AllowlistsAreLive(t *testing.T) {
	conversions, membership, parallel := vocabScan(t)

	live := func(keys []string) map[string]bool {
		m := make(map[string]bool, len(keys))
		for _, k := range keys {
			m[k] = true
		}
		return m
	}
	conversionKeys := make([]string, 0, len(conversions))
	for _, c := range conversions {
		conversionKeys = append(conversionKeys, c.key())
	}
	membershipKeys := make([]string, 0, len(membership))
	for _, m := range membership {
		membershipKeys = append(membershipKeys, m.key())
	}
	parallelKeys := make([]string, 0, len(parallel))
	for _, p := range parallel {
		parallelKeys = append(parallelKeys, p.key())
	}

	for _, tc := range []struct {
		name    string
		allowed map[string]string
		live    map[string]bool
	}{
		{"vocabConversionAllowed", vocabConversionAllowed, live(conversionKeys)},
		{"vocabMembershipAllowed", vocabMembershipAllowed, live(membershipKeys)},
		{"vocabParallelAllowed", vocabParallelAllowed, live(parallelKeys)},
	} {
		keys := make([]string, 0, len(tc.allowed))
		for k := range tc.allowed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !tc.live[k] {
				t.Errorf("%s allows %q (%s) but the scan no longer reports that site — delete the "+
					"entry, or it will silently exempt whatever lands there next", tc.name, k, tc.allowed[k])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The ratchet
// ---------------------------------------------------------------------------

// vocabConversionAllowed is the RAW CONVERSION rule's shrinking allowlist, in
// the same shape as write_discipline_test.go's writeDisciplineAllowed: a
// durable key ("file.go#Symbol#owner.Vocabulary", where Symbol is
// "Type.Method" for a method and the bare function name otherwise) mapped to
// the fix required to remove the entry. Generated MECHANICALLY by running the
// gate with an empty map and transcribing every reported site.
var vocabConversionAllowed = map[string]string{
	"cmd/ltk/check.go#checkFlags.run#internal/ltk/ir.Shell":       "the --shell flag value is asserted into ir.Shell; internal/ltk/ir declares the vocabulary but ships no parser for it — add one and call it here (shellenv.ShellFromPath is the nearest existing membership decision)",
	"cmd/ltk/evaluate.go#evaluateFlags.run#internal/ltk/ir.Shell": "same --shell assertion as cmd/ltk/check.go; both wait on a parser in internal/ltk/ir",

	"internal/agentcoord/coord/enginehost.go#EngineHost.startRun#internal/transcript.RawPolicy": "raw-transcript policy string asserted into the enum; internal/transcript ships no parser for RawPolicy — add one and call it",
	"internal/lm/grpc/chat.go#GRPCClient.openRecorder#internal/transcript.RawPolicy":            "same RawPolicy assertion as coord.EngineHost.startRun, reached from the wire side",

	"internal/lm/grpc/chat.go#chatStartFromProto#internal/shared/agent.MCPTransport":            "a proto string field asserted into the transport enum; an unknown wire value becomes a well-typed value nothing rejects",
	"internal/lm/grpc/sessionhistory.go#entryFromProto#internal/shared/agent.SessionEntryType":  "a proto string field asserted into the entry-type enum; same unchecked-wire-value shape",
	"internal/lm/grpc/sessionhistory.go#entryFromProto#internal/shared/agent.SessionSystemKind": "a proto string field asserted into the system-kind enum; same unchecked-wire-value shape",
	"internal/transcript/history.go#entriesFromRecord#internal/shared/agent.SessionEntryType":   "a stored record's string asserted into the entry-type enum; same unchecked-input shape as the grpc side",
	"internal/transcript/history.go#entriesFromRecord#internal/shared/agent.SessionSystemKind":  "a stored record's string asserted into the system-kind enum; same unchecked-input shape as the grpc side",

	"internal/operations/agents.go#SetAgent#internal/agents.DrivingMode":          "a user-set config value asserted into the driving-mode enum; internal/agents ships no parser for DrivingMode",
	"internal/operations/agents.go#validateAgentAxes#internal/agents.DrivingMode": "same DrivingMode assertion inside the routine that is supposed to be VALIDATING the axes",

	"internal/operations/countersign_records.go#countersignRecords.Approved#internal/signing.Form": "a stored record's form string asserted into signing.Form; internal/signing ships no parser for it",
	"internal/operations/review.go#reviewEnumerator.classify#internal/signing.Form":                "same signing.Form assertion from the review side",
	"internal/operations/review.go#reviewEnumerator.classify#internal/bundles.ContentForm":         "a stored string asserted into bundles.ContentForm; internal/bundles ships no parser for it",

	"internal/operations/signable.go#bundleSignable.Kind#internal/trust.ItemKind": "MINTS trust.ItemKind(\"bundle\"), a value outside the declared set — the call site's own comment records that no constant names a whole bundle. Either declare it or model a whole bundle as a different type; today the trust tier sees a kind its own vocabulary does not contain",

	"internal/taskloom/config/config.go#Config.ResolveMode#internal/shared/tasks/paths.Mode": "a config string asserted into paths.Mode; internal/shared/tasks/paths ships no parser for it",
}

// vocabMembershipAllowed is the DUPLICATED MEMBERSHIP TEST rule's shrinking
// allowlist, keyed "file.go#Symbol#owner.Vocabulary".
var vocabMembershipAllowed = map[string]string{
	"internal/cli/completion.go#runCompletion#internal/ltk/ir.Shell":        "re-tests ir.Shell members to pick a completion script; the shell vocabulary has three independent membership tests (here, ltk/rules.shellForProgram, ltk/shellenv.ShellFromPath) and no owner-side parser to route them through",
	"internal/ltk/rules/rules.go#shellForProgram#internal/ltk/ir.Shell":     "second of the three parallel shell-vocabulary membership tests; waits on a parser in internal/ltk/ir",
	"internal/ltk/shellenv/shellenv.go#ShellFromPath#internal/ltk/ir.Shell": "third of the three parallel shell-vocabulary membership tests; the widest of them, and the natural place to consolidate the other two",

	"internal/cli/search.go#resolveSearchTypes#internal/operations.ItemKind":    "re-spells the fragment/command item-kind vocabulary, which is DECLARED TWICE ALREADY (operations.ItemKind and operations.DistillKind are byte-identical two-member enums in one package) and a third time as trust.ItemKind. Consolidating those is the fix; this entry is the consumer that made the split visible",
	"internal/cli/search.go#resolveSearchTypes#internal/operations.DistillKind": "same site, matching operations.DistillKind — the identical twin of operations.ItemKind",
	"internal/cli/search.go#resolveSearchTypes#internal/trust.ItemKind":         "same site, matching trust.ItemKind, the third declaration of the item-kind vocabulary",

	"internal/cli/session_watch.go#renderWatchEntryText#internal/shared/agent.SessionEntryType":                     "re-spells SessionEntryType members instead of comparing against the exported constants; the entry-type vocabulary has eight such copies across cli, cli/tui, liveness and the two vendor readers",
	"internal/cli/tui/render.go#roleTag#internal/shared/agent.SessionEntryType":                                     "re-spells five SessionEntryType members; the widest copy",
	"internal/cli/tui/render.go#itemBodyLines#internal/shared/agent.SessionEntryType":                               "re-spells three SessionEntryType members",
	"internal/liveness/transcript.go#txScan.entry#internal/shared/agent.SessionEntryType":                           "re-spells three SessionEntryType members",
	"internal/liveness/transcript.go#txTail.entry#internal/shared/agent.SessionEntryType":                           "re-spells two SessionEntryType members",
	"internal/transcript/vendorreader/claude/session.go#convertLines#internal/shared/agent.SessionEntryType":        "re-spells two SessionEntryType members while reading a vendor format",
	"internal/transcript/vendorreader/claude/session.go#sessionScan.observe#internal/shared/agent.SessionEntryType": "re-spells two SessionEntryType members while reading a vendor format",
	"internal/transcript/vendorreader/claude/session.go#messageEntries#internal/shared/agent.SessionEntryType":      "re-spells four SessionEntryType members while reading a vendor format",
	"internal/transcript/vendorreader/codex/rollout.go#messageEvents#internal/shared/agent.SessionEntryType":        "re-spells two SessionEntryType members while reading a vendor format",

	"internal/liveness/transcript.go#txScan.line#internal/transcript.Kind":     "re-spells transcript.Kind members rather than comparing against internal/transcript's own constants, which this package already imports",
	"internal/liveness/transcript.go#txScan.tailLine#internal/transcript.Kind": "same transcript.Kind re-spelling in the tail path",

	"internal/trust/itemref.go#ParseSelector#internal/shared/ledger.Surface":  "the selector parser re-spells four ledger.Surface members. The engine-surface vocabulary is declared twice — ledger.Surface and agent.ProbeKind overlap on mcp/commands/skills/context — so there is no single owner to route through yet; consolidating those two is the fix",
	"internal/trust/itemref.go#ParseSelector#internal/shared/agent.ProbeKind": "same site, matching the second declaration of the engine-surface vocabulary",
}

// vocabParallelAllowed is the PARALLEL LIST rule's shrinking allowlist, keyed
// by the two colliding symbols in sorted order.
var vocabParallelAllowed = map[string]string{

	"internal/cli/tui/render.go#roleTag ~ internal/transcript/vendorreader/claude/session.go#messageEntries": "two copies of the session-entry-type list; both are also flagged individually against agent.SessionEntryType, which is the owner they should route through",

	"internal/ltk/rules/rules.go#shellForProgram ~ internal/ltk/shellenv/shellenv.go#ShellFromPath": "two copies of the shell-name list, ten literals wide — wider than ir.Shell's declared constants, so the undeclared spellings (ksh, ksh93, powershell, .exe) live only in these two functions and match only by luck",

	"internal/content/treefs.go#validateTreePath ~ internal/remote/reference.go#validateItemPath": "two copies of the path-segment rejection list (., .., /, \\). Not a user-typeable vocabulary, but a genuinely duplicated security-relevant decision: extract one shared path-segment validator rather than keep two",
}
