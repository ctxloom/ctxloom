//go:build arch

package arch

import (
	"go/ast"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// one-launch-constructor: the resolved launch has ONE
// constructor, launch.Resolve. Outside its own package and the wire codec
// that decodes one back, no production code BUILDS a launch.Launch — by
// composite literal with fields, by new, or by declaring a variable of the
// type to fill in — because a Launch someone assembled by hand carries no
// guarantee Resolve makes (the floor applied, the cell prepared, the
// endpoint minted once). A zero-value `launch.Launch{}` on an error return
// constructs nothing and is not a site.

// launchConstructorHomes are the directories that may construct a Launch:
// the resolver, and the codec that decodes the wire form.
var launchConstructorHomes = []string{"internal/core/launch", "internal/adapters/coordgrpc"}

// oneLaunchConstructorAllowed is the rule's shrinking allowlist: "file.go"
// mapped to the slice in which the site leaves. Empty: the rule holds.
var oneLaunchConstructorAllowed = map[string]string{}

// isLaunchType reports whether expr spells launch.Launch — qualified from
// another package, or bare inside package launch itself.
func isLaunchType(expr ast.Expr, inLaunchPkg bool) bool {
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && pkg.Name == "launch" && x.Sel.Name == "Launch"
	case *ast.Ident:
		return inLaunchPkg && x.Name == "Launch"
	}
	return false
}

func scanLaunchConstructions(t *testing.T) []ringSite {
	t.Helper()
	var out []ringSite
	var seen int
	walkRingFiles(t, func(rf ringFile) {
		inLaunch := rf.f.Name.Name == "launch"
		ast.Inspect(rf.f, func(n ast.Node) bool {
			what := launchConstruction(n, inLaunch)
			if what == "" {
				return true
			}
			seen++
			if archrules.UnderAny(rf.dir, launchConstructorHomes) {
				return true
			}
			out = append(out, ringSite{file: rf.rel, what: what, line: rf.fset.Position(n.Pos()).Line})
			return true
		})
	})
	if seen == 0 {
		t.Fatal("the walk found no launch.Launch construction anywhere, not even in the resolver — the spelling this rule keys on is stale, not the module clean")
	}
	return out
}

// launchConstruction names how n builds a launch.Launch — by composite
// literal with fields, by new, or by declaring a variable of the type to
// fill in — or "" when it builds none.
func launchConstruction(n ast.Node, inLaunch bool) string {
	switch x := n.(type) {
	case *ast.CompositeLit:
		if isLaunchType(x.Type, inLaunch) && len(x.Elts) > 0 {
			return "builds a launch.Launch by composite literal"
		}
	case *ast.CallExpr:
		if newsLaunch(x, inLaunch) {
			return "builds a launch.Launch with new"
		}
	case *ast.ValueSpec:
		if x.Type != nil && isLaunchType(x.Type, inLaunch) && len(x.Values) == 0 {
			return "declares a launch.Launch variable to fill in"
		}
	}
	return ""
}

// newsLaunch reports whether call is new(launch.Launch).
func newsLaunch(call *ast.CallExpr, inLaunch bool) bool {
	fn, ok := call.Fun.(*ast.Ident)
	return ok && fn.Name == "new" && len(call.Args) == 1 && isLaunchType(call.Args[0], inLaunch)
}

// TestArch_OneLaunchConstructor is the gate: outside core/launch and the
// wire codec, no production code constructs a launch.Launch.
func TestArch_OneLaunchConstructor(t *testing.T) {
	checkRingAllowlist(t, "one-launch-constructor", scanLaunchConstructions(t), oneLaunchConstructorAllowed,
		"launch.Resolve is the one constructor; a launch assembled by hand carries none of its guarantees")
}

func TestArch_OneLaunchConstructor_AllowlistIsLive(t *testing.T) {
	checkRingAllowlistIsLive(t, "one-launch-constructor", scanLaunchConstructions(t), oneLaunchConstructorAllowed)
}
