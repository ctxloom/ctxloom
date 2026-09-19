//go:build arch

// DEGRADING MAY NEVER DAMAGE, AND MAY NEVER GRANT A SECURITY BYPASS.
//
// --degraded exists so a broken environment still yields a working agent. It
// does NOT exist to turn a refusal into a permission. The discriminator that
// governs every site is the one strictness.Finding.NonDegradable states: does
// LAUNCHING cause the harm? If it does, degraded mode must still refuse, and
// the refusal must carry the fix.
//
// WHY THIS IS A GATE AND NOT A CONVENTION. The rule existed in this module for
// months, unevenly and unstated: signing had ruled that --degraded does not
// bypass, one operations gate documented four decisions the flag "plays NO
// role in" — and meanwhile a requested container runtime fell back to the
// HOST under --degraded, silently supplying an agent with none of the process
// isolation it asked for, while the same code refused the strictly SMALLER
// substitution of one container ownership mode for the other. Each site had
// been decided from scratch by whoever wrote it, because nothing stated the
// rule and nothing checked it; a fresh degraded site landed AFTER the
// principle was ruled. The audit that followed classified every site and
// converted the damaging ones. What an audit cannot leave behind is a way to
// catch the NEXT one: the sites it corrected stay correct, and nothing notices
// the site added the following week. This gate is that rule.
//
// WHY A CENSUS AND NOT A CLASSIFIER. Whether a particular degrade DAMAGES is a
// semantic question — it depends on what the branch goes on to do, what the
// caller was promised, and whether work would be lost. No syntactic check can
// answer it, and a check that pretended to would be worse than none: a green
// gate asserting a security property it cannot actually see is exactly the
// shape of a defence that is believed and does not hold.
//
// So this gate does the one thing a syntactic check CAN do honestly: it pins
// WHERE the mode is consulted, and HOW MANY TIMES per file. A new consultation
// fails the build, and the author clears it by writing down why theirs does
// not damage. That moves the judgement to the moment the code is written, in
// front of the person who knows the answer, instead of to the next audit. The
// reason is the entire value of an entry: a bare path would record that
// somebody once looked, not what they concluded, and it is what a later
// reader checks the branch against once the code around it has changed.
//
// WHY THE COUNT AND NOT JUST THE FILE. The likeliest place for a new degrade
// branch is beside an existing one — the files already on the allowlist are
// the ones full of strictness.Fail sites, each a candidate for someone to
// add "if Degraded() { return nil }" under. A file-set census waves that
// through. Pinning the per-file count makes it red, at the cost of a stale
// count when an existing branch is split or merged; that cost is the point,
// since the author then rewrites the reason to cover the new shape.
//
// Detection is purely syntactic (go/ast, no go/types). The mode is a VALUE
// (strictness.Mode) a composition holds, so a branch on it is a READ of the
// mode's Degraded field off a composition — the selector chain
// `<x>.Strictness.Degraded` — or, defensively, any CALL to something named
// "Degraded" (the retired process global's shape). It deliberately does NOT
// match the identifier in prose — most textual mentions of Degraded in this
// module are comments WARNING against branching on it, and a grep-shaped
// census would count the warnings as instances of the thing they warn about.
// A read through a local variable of type Mode is not seen; the sanctioned
// readers below are the only ones that exist, and strictness.Mode's own
// methods (the defining file) are where the value is consulted for the gates.
//
// KNOWN BLIND SPOTS, stated so nobody reads a green run as more than it is:
//
//   - It cannot see the DAMAGE. An allowlisted branch that is later edited to
//     do something worse, at the same call count, passes. The reason string
//     is the only defence there, and it is prose — which is why each entry
//     must name what the branch does, not merely that it is fine.
//   - It counts consultations of the mode, not decisions downgraded by it.
//     The mode is also applied by strictness.Actionable on behalf of every
//     strictness.Fail site; a Fail site whose finding should have been
//     NonDegradable is invisible here, and is governed by the per-site tests
//     the audit left behind.
//   - A route to the mode that never spells "Degraded" — a raw CTXLOOM_DEGRADED
//     env read, or a bool captured once and threaded through parameters — is
//     not counted past the point of capture. The composition root (the cli's
//     strictnessMode, taskloom's) is where the value is built, and it builds
//     rather than branches.
package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// degradeExemption is one allowlisted file: how many times it consults the
// mode, and why none of those consultations damages or bypasses.
type degradeExemption struct {
	// sites is the number of Degraded() calls the file is permitted to make.
	// One more is a new branch that needs its own written reason.
	sites int
	// why names what each branch does under --degraded and why launching in
	// that state causes no harm. It is checked by the next reader, not by
	// this gate.
	why string
}

// degradeBranchAllowed are the module-relative files permitted to consult the
// degraded mode.
var degradeBranchAllowed = map[string]degradeExemption{
	"internal/adapters/cli/run.go": {sites: 1, why: "READS the composition's mode once to hand it to the " +
		"launch resolver as a VALUE (launch.Source.Degraded): the resolver holds no strictness, " +
		"and its degraded arms only narrow — a posture that does not parse lands on " +
		"PermissionFloor, a child that would block on a prompt launches at PermissionFloor, a " +
		"missing default agent launches context-free — so nothing is granted that strict mode " +
		"withholds"},
}

// TestArch_DegradeDiscipline_EveryBranchIsJustified fails when production code
// consults the degraded mode from a file that has not written down why its
// branch is safe, or consults it more times than the file's entry permits.
func TestArch_DegradeDiscipline_EveryBranchIsJustified(t *testing.T) {
	for file, lines := range findDegradeBranches(t) {
		ex, ok := degradeBranchAllowed[file]
		if ok && len(lines) == ex.sites {
			continue
		}
		what := "is not on the degrade allowlist"
		if ok {
			what = fmt.Sprintf("is allowlisted for %d call(s) but now makes %d", ex.sites, len(lines))
		}
		assert.Failf(t, "unjustified branch on degraded mode",
			"%s calls Degraded() at line(s) %v, and %s.\n\n"+
				"Degrading may never damage and may never grant a security bypass. The test is "+
				"strictness.Finding.NonDegradable's: does LAUNCHING cause the harm? If it does, "+
				"degraded mode must still refuse — do not touch the allowlist, fix the branch.\n\n"+
				"If launching is genuinely safe, set this file's entry in degradeBranchAllowed to "+
				"the new count WITH a reason that covers every branch. The reason is the point: it "+
				"is what a later reader checks the branch against when the code around it has changed.",
			file, fmt.Sprint(lines), what)
	}
}

// TestArch_DegradeAllowlist_IsLive fails when an allowlisted file stops
// consulting the mode, so an exemption cannot outlive the branch it excused.
// A stale entry is worse than a missing one: it reads as a considered
// judgement about code that is no longer there. A count that has DROPPED but
// not to zero is reported by the sibling test, since the reason then covers
// a branch that no longer exists.
func TestArch_DegradeAllowlist_IsLive(t *testing.T) {
	found := findDegradeBranches(t)
	for file, ex := range degradeBranchAllowed {
		assert.Containsf(t, found, file,
			"%s is on the degrade allowlist (%q) but no longer calls Degraded() — drop the entry",
			file, ex.why)
	}
}

// findDegradeBranches walks every non-test .go file under the module root and
// returns, per module-relative file, the sorted line numbers at which it CALLS
// something named "Degraded" — package-qualified or bare.
func findDegradeBranches(t *testing.T) map[string][]int {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	hits := map[string][]int{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", p, perr)
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if callsDegraded(x) {
					hits[rel] = append(hits[rel], fset.Position(x.Pos()).Line)
				}
			case *ast.SelectorExpr:
				if readsCompositionMode(x) {
					hits[rel] = append(hits[rel], fset.Position(x.Pos()).Line)
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	for f := range hits {
		sort.Ints(hits[f])
	}
	return hits
}

// callsDegraded reports whether a call's callee is spelled "Degraded", either
// as the selector of a qualified expression (strictness.Degraded, or any
// alias the importer chose) or as a bare identifier inside the package that
// defines it.
func callsDegraded(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel != nil && fn.Sel.Name == "Degraded"
	case *ast.Ident:
		return fn.Name == "Degraded"
	}
	return false
}

// readsCompositionMode reports whether sel is `<x>.Strictness.Degraded`: a
// read of a composition's mode value, the shape a gate would branch on.
func readsCompositionMode(sel *ast.SelectorExpr) bool {
	if sel.Sel == nil || sel.Sel.Name != "Degraded" {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel != nil && inner.Sel.Name == "Strictness"
}
