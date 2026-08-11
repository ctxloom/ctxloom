//go:build acceptance && integration && coveragegate

package acceptance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The FLAG-LEVEL completeness gate, the sibling of
// cli_coverage_gate_test.go's leaf-level one. That gate asks "did this
// command's code run at all"; this one asks "was this FLAG ever passed",
// which a leaf can answer yes to while every optional flag on it stays
// permanently untried.
//
// It reads the same textfmt profile under the same build tag and the same
// second `go test` invocation, for the same reason: coverage is complete only
// once every instrumented exec has flushed.
//
// WHY IT IS KEYED BY EXECUTION AND NOT BY SOURCE PATH. The two gates that
// preceded it are both keyed by where code LIVES — a command path, a file
// name. A mass file move is therefore the one change that can silently drop
// coverage past them: a leaf that MOVED and a leaf that STOPPED BEING
// EXERCISED look identical to a path-keyed census. Here the census is
// re-derived from the tree on every run and the credit comes from counters, so
// a move relocates a site's key and its evidence together. What CANNOT survive
// a move is an entry in flagCoverageExempt below, which names a file — and
// that is deliberate: a stale exemption fails loudly (see the third direction
// in the test) rather than silencing a site at its new address.
//
// WHAT "EXERCISED" MEANS HERE, EXACTLY. Only the `if`-guarded shape can be
// measured: `if cmd.Flags().Changed("x") { ... }` compiles to a block whose
// counter is incremented by the body running, and the body runs only when the
// flag was passed. Every other shape is reported BY NAME as unmeasurable
// rather than skipped, because a census that quietly drops what it cannot
// measure reports a completeness it never checked.

// flagSite is one `Changed()` call in the CLI source.
type flagSite struct {
	File    string // module-relative, "internal/cli/root.go"
	Line    int    // line of the Changed() call
	Flag    string // the literal flag name
	Func    string // enclosing function, for the failure message
	Guarded bool   // the call IS an `if` condition (its body is the evidence)

	// Position of the guarded body's opening brace, which is where Go's cover
	// tool starts that block. NOT derivable from Line: a condition may wrap
	// across lines, and consecutive `if`s put two different blocks on one
	// line. Only meaningful when Guarded.
	bodyLine int
	bodyCol  int

	// Why the site is not Guarded, phrased for the reader of a test log.
	// Empty when Guarded.
	unmeasurable string
}

// key names a site the way flagCoverageExempt does.
func (s flagSite) key() string { return s.File + ":--" + s.Flag }

// where renders the site's source position and enclosing function.
func (s flagSite) where() string {
	return fmt.Sprintf("%s:%d in %s()", s.File, s.Line, s.Func)
}

// flagCoverageExempt are guarded flag sites no hermetic scenario reaches, each
// with the reason. Same contract as coverageExemptLeaves: the gate fails on a
// difference in EITHER direction, so an entry that starts being covered has to
// come out — and, additionally, an entry naming a site the census no longer
// finds fails too, so a rename or a file move cannot leave a silencer behind.
var flagCoverageExempt = map[string]string{
	"internal/cli/root.go:--degraded": "its only exerciser self-skips wherever a container runtime is reachable, " +
		"so on a developer machine it can never run; tracked as taskloom antiviral-rebirth",
}

// unmeasurableShape describes why a Changed() call cannot be credited from
// coverage. Each is a real limit of the technique, not a category of site that
// matters less.
const (
	shapeValueForm = "read into a value rather than tested as an `if` condition, so it has no " +
		"cover block of its own — nothing in the profile distinguishes the flag being passed from not"
	shapeNegated = "the `if` is NEGATED (`!Changed`), so its body running proves the flag was ABSENT. " +
		"The inverse is not recoverable either: this profile is mode=set aggregated over the whole " +
		"suite, so one scenario omitting the flag sets the counter for all of them"
	shapeIndirect = "the call is inside an `if` condition but not as a positive top-level conjunct " +
		"(a disjunction or a negation sits above it), so the body running does not prove the flag was passed"
)

func TestCLICoverage_EveryChangedFlagWasPassed(t *testing.T) {
	profile := os.Getenv(coverProfileEnv)
	if profile == "" {
		t.Fatalf("%s is unset: this gate reads a textfmt coverage profile and cannot "+
			"infer one. Run it through `just test-acceptance-cover`, which produces the "+
			"profile and then invokes this, or `just test-coverage-gate` against an "+
			"existing one. Skipping instead of failing would report a clean gate over no "+
			"data at all.", coverProfileEnv)
	}
	blocks, err := readCoverProfile(profile)
	if err != nil {
		t.Fatalf("read coverage profile %s: %v", profile, err)
	}
	if len(blocks) == 0 {
		t.Fatalf("coverage profile %s contains no blocks — the instrumented binary "+
			"produced no data, so this gate measured nothing", profile)
	}

	srcDir := cliSourceDir(t)
	sites, err := changedFlagSites(srcDir)
	if err != nil {
		t.Fatalf("census the Changed() sites under %s: %v", srcDir, err)
	}
	// A census of nothing is the failure this project is named for: a clean
	// gate over zero findings. The CLI has had these sites since before this
	// gate existed; an empty result means the walk broke, not that the idiom
	// went away.
	if len(sites) == 0 {
		t.Fatalf("no Changed() call sites found under %s — the census read the tree and "+
			"came back empty, which is a broken walk rather than a clean bill of health", srcDir)
	}

	seen := map[string]flagSite{}
	var unmeasurable, uncovered, unexpectedlyCovered []string
	measured, exercisedCount := 0, 0

	for _, s := range sites {
		k := s.key()

		// Unmeasurable sites never consult flagCoverageExempt, so two of them
		// (or one unmeasurable and one guarded) sharing a key is not the
		// "one exemption speaks for two sites" hazard the dup check below
		// exists to catch — e.g. manage.go's --engine is read once as a value
		// (line 93, unmeasurable) and re-checked as an `if` guard later in the
		// same function (line 153, measurable) for an unrelated, independently
		// documented reason. Report it and move on without touching `seen`, so
		// the guarded sibling can still claim the key for itself below.
		if !s.Guarded {
			unmeasurable = append(unmeasurable,
				fmt.Sprintf("%s (%s): %s", k, s.where(), s.unmeasurable))
			continue
		}

		if prev, dup := seen[k]; dup {
			// Two GUARDED sites sharing a key would share one exemption, so
			// exempting either would silence both.
			t.Errorf("%s names two distinct guarded sites (%s and %s). The exemption map is "+
				"keyed by file and flag, so it cannot speak about one without the other.",
				k, prev.where(), s.where())
			continue
		}
		seen[k] = s
		measured++

		exercised, found := flagSiteExercised(s, blocks)
		if !found {
			t.Errorf("%s (%s): its guarded block is not in the coverage profile at "+
				"%s:%d.%d. A site whose code cannot be located is not one this gate can "+
				"vouch for.", k, s.where(), s.File, s.bodyLine, s.bodyCol)
			continue
		}
		if exercised {
			exercisedCount++
		}
		_, exempt := flagCoverageExempt[k]
		switch {
		case !exercised && !exempt:
			uncovered = append(uncovered, fmt.Sprintf("%s (%s)", k, s.where()))
		case exercised && exempt:
			unexpectedlyCovered = append(unexpectedlyCovered, fmt.Sprintf("%s (%s)", k, s.where()))
		}
	}

	sort.Strings(uncovered)
	sort.Strings(unexpectedlyCovered)
	sort.Strings(unmeasurable)

	for _, p := range uncovered {
		t.Errorf("%s: the acceptance suite never passed this flag — its guarded body did not "+
			"run once in the whole suite. Give it a scenario that asserts the EFFECT of "+
			"passing it, or add it to flagCoverageExempt with the reason.", p)
	}
	// Second direction, so the list cannot rot into a silencer.
	for _, p := range unexpectedlyCovered {
		t.Errorf("%s is in flagCoverageExempt but its guarded body RAN. Remove the entry: an "+
			"exemption that no longer applies hides the next real gap.", p)
	}
	// Third direction, and the one a mass file move trips: an exemption whose
	// site the census cannot find any more. Without this, moving a file leaves
	// an entry that silences nothing and says nobody noticed.
	var stale []string
	for k := range flagCoverageExempt {
		if _, ok := seen[k]; !ok {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		t.Errorf("%s is in flagCoverageExempt but the census finds no such site. The flag was "+
			"renamed, removed, or its file MOVED — re-key the entry to where the site lives "+
			"now, or delete it. An exemption pointing at nothing is not a passing site.", k)
	}

	// Reported, never silently dropped: these are the sites this technique
	// cannot speak for, so the number above is coverage of `measured`, not of
	// every flag the CLI reads.
	t.Logf("flag census: %d Changed() sites, %d measurable, %d exercised, %d exempt, %d unmeasurable",
		len(sites), measured, exercisedCount, len(flagCoverageExempt), len(unmeasurable))
	for _, u := range unmeasurable {
		t.Logf("UNMEASURABLE %s", u)
	}
}

// flagSiteExercised reports whether the site's guarded body ran, and whether
// the block could be located in the profile at all.
//
// The join is by FILE, LINE AND COLUMN. The column is not decoration: for the
// second of two consecutive `if`s, the block that merely REACHES the `if`
// starts on the same line as the body it guards, and it runs whenever control
// gets there regardless of the condition. Matching on line alone would let it
// answer for the body, turning every such site into an unconditional pass.
func flagSiteExercised(s flagSite, blocks map[string][]coverBlock) (exercised, found bool) {
	for _, b := range blocks[s.File] {
		if b.startLine == s.bodyLine && b.startCol == s.bodyCol {
			return b.count > 0, true
		}
	}
	return false, false
}

// changedFlagSites parses every non-test Go file under srcDir and returns one
// flagSite per `…Changed("name")` call, in source order.
//
// It ERRORS rather than skipping on a shape it cannot name — a call outside
// any function declaration, or one whose flag name is not a string literal.
// Both are writable Go; neither exists today; either would otherwise shrink
// the census without saying so.
func changedFlagSites(srcDir string) ([]flagSite, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	root, err := moduleRootFrom(srcDir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	var out []flagSite
	for _, name := range names {
		path := filepath.Join(srcDir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)

		// Every Changed() call in the file, so a call that no function
		// declaration claims can be reported rather than lost.
		all := map[*ast.CallExpr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && isChangedCall(c) {
				all[c] = true
			}
			return true
		})

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			sites, claimed, err := sitesInFunc(fset, fn, rel)
			if err != nil {
				return nil, err
			}
			for c := range claimed {
				delete(all, c)
			}
			out = append(out, sites...)
		}
		for c := range all {
			return nil, fmt.Errorf("%s: a Changed() call at line %d sits outside any function "+
				"declaration; the census cannot name its enclosing function and will not "+
				"guess", rel, fset.Position(c.Pos()).Line)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// sitesInFunc classifies every Changed() call inside fn, returning the sites
// and the set of calls it accounted for.
func sitesInFunc(fset *token.FileSet, fn *ast.FuncDecl, rel string) ([]flagSite, map[*ast.CallExpr]bool, error) {
	// Guarded calls first: an `if` whose condition holds the call as a
	// positive top-level conjunct, so the body running means the flag was
	// passed. Recorded with the brace position, which is where the cover tool
	// starts that block.
	type guard struct{ line, col int }
	guards := map[*ast.CallExpr]guard{}
	// A call under an `if` in any other shape — negated, or one arm of a
	// disjunction — where the body running proves nothing about the flag.
	indirect := map[*ast.CallExpr]string{}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Cond == nil {
			return true
		}
		brace := fset.Position(ifStmt.Body.Lbrace)
		ast.Inspect(ifStmt.Cond, func(cn ast.Node) bool {
			c, ok := cn.(*ast.CallExpr)
			if !ok || !isChangedCall(c) {
				return true
			}
			if positiveConjunct(ifStmt.Cond, c) {
				guards[c] = guard{line: brace.Line, col: brace.Column}
			} else if negates(ifStmt.Cond, c) {
				indirect[c] = shapeNegated
			} else {
				indirect[c] = shapeIndirect
			}
			return true
		})
		return true
	})

	var out []flagSite
	claimed := map[*ast.CallExpr]bool{}
	var walkErr error
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || !isChangedCall(c) || claimed[c] {
			return true
		}
		flag, ok := literalFlagName(c)
		if !ok {
			walkErr = fmt.Errorf("%s:%d: Changed() is called with a non-literal flag name in "+
				"%s(); the census cannot report a flag it cannot name",
				rel, fset.Position(c.Pos()).Line, fn.Name.Name)
			return false
		}
		claimed[c] = true
		s := flagSite{
			File: rel,
			Line: fset.Position(c.Pos()).Line,
			Flag: flag,
			Func: fn.Name.Name,
		}
		if g, isGuard := guards[c]; isGuard {
			s.Guarded = true
			s.bodyLine, s.bodyCol = g.line, g.col
		} else if why, isIndirect := indirect[c]; isIndirect {
			s.unmeasurable = why
		} else {
			s.unmeasurable = shapeValueForm
		}
		out = append(out, s)
		return true
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	return out, claimed, nil
}

// positiveConjunct reports whether call is a top-level conjunct of cond that
// nothing negates — the only shape in which "the body ran" means "the flag was
// passed". `a && Changed(x)` qualifies; `!Changed(x)` and `a || Changed(x)` do
// not, because their bodies run in cases where the flag was never passed.
func positiveConjunct(cond ast.Expr, call *ast.CallExpr) bool {
	switch e := cond.(type) {
	case *ast.ParenExpr:
		return positiveConjunct(e.X, call)
	case *ast.BinaryExpr:
		if e.Op == token.LAND {
			return positiveConjunct(e.X, call) || positiveConjunct(e.Y, call)
		}
		return false
	default:
		return cond == ast.Expr(call)
	}
}

// negates reports whether a logical NOT stands between cond's root and call,
// so the failure message can say which shape it is rather than lumping every
// unmeasurable guard together.
func negates(cond ast.Expr, call *ast.CallExpr) bool {
	found := false
	var walk func(e ast.Expr, under bool)
	walk = func(e ast.Expr, under bool) {
		switch x := e.(type) {
		case nil:
			return
		case *ast.ParenExpr:
			walk(x.X, under)
		case *ast.UnaryExpr:
			if x.Op == token.NOT {
				walk(x.X, true)
				return
			}
			walk(x.X, under)
		case *ast.BinaryExpr:
			walk(x.X, under)
			walk(x.Y, under)
		default:
			if e == ast.Expr(call) && under {
				found = true
			}
		}
	}
	walk(cond, false)
	return found
}

// isChangedCall reports whether c is a `<something>.Changed(<one arg>)` call.
// Both `cmd.Flags().Changed(…)` and `cmd.Root().PersistentFlags().Changed(…)`
// match; the receiver is deliberately not constrained, because a census that
// only recognised one spelling would miss the other.
func isChangedCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "Changed" && len(c.Args) == 1
}

// literalFlagName returns the string literal c was called with.
func literalFlagName(c *ast.CallExpr) (string, bool) {
	lit, ok := c.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return name, true
}

// cliSourceDir locates internal/cli relative to THIS source file, the
// precedent steps_j001000_transcript_capture.go sets. The working directory of
// a `go test` run is the test's own package, which says nothing about where
// the module is checked out.
func cliSourceDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller could not locate this test file, so the census has no tree to read")
	}
	root, err := moduleRootFrom(filepath.Dir(thisFile))
	if err != nil {
		t.Fatalf("locate the module root from %s: %v", thisFile, err)
	}
	dir := filepath.Join(root, "internal", "cli")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the CLI source at %s is unreadable (%v); this gate censuses the tree and "+
			"cannot proceed over an absent one", dir, err)
	}
	return dir
}

// moduleRootFrom walks up from dir to the directory holding go.mod.
func moduleRootFrom(dir string) (string, error) {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no go.mod at or above %s", dir)
		}
		d = parent
	}
}
