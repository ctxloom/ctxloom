package archlint

import (
	"fmt"
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// LayeringAnalyzer enforces archrules.LayeringRules: no production file in a
// From-subtree package may import a Forbid-subtree package (outside the rule's
// Except prefixes) unless that edge is named in the rule's allowlist.
// Depending on genuinely shared packages outside both subtrees is unaffected —
// only imports resolving under Forbid are checked.
//
// The liveness half judges only edges whose from-package is the one under
// analysis: an entry naming a package that no longer exists is invisible to a
// per-package pass, and tests/arch's TestArch_LayeringAllowlist_IsLive is what
// catches that shape.
var LayeringAnalyzer = &analysis.Analyzer{
	Name: "archlayering",
	Doc:  "package subtrees must not import the subtrees the layering table forbids them",
	Run:  func(pass *analysis.Pass) (any, error) { return runLayering(pass, archrules.LayeringRules) },
}

// runLayering checks the package under analysis against rules. Production
// passes archrules.LayeringRules; the parameter is what lets a test reach every
// branch with a table written for it.
func runLayering(pass *analysis.Pass, rules []archrules.LayeringRule) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" {
		return nil, nil
	}
	imports := ImportPaths(pass)
	imported := importedDirs(imports)
	for _, rule := range rules {
		if rule.MatchesFrom(dir) {
			reportForbiddenImports(pass, rule, dir, imports)
		}
		if allowlistLivenessEnabled() {
			reportStaleEdges(pass, rule, dir, imported)
		}
	}
	return nil, nil
}

// importedDirs is the set of module-local directories the package imports.
func importedDirs(imports map[string]*ast.ImportSpec) map[string]bool {
	out := map[string]bool{}
	for ip := range imports {
		if dep := LocalDir(ip); dep != "" {
			out[dep] = true
		}
	}
	return out
}

// reportForbiddenImports reports each import the rule forbids from dir whose
// edge is not in the rule's Allowed map.
func reportForbiddenImports(pass *analysis.Pass, rule archrules.LayeringRule, dir string, imports map[string]*ast.ImportSpec) {
	for ip, spec := range imports {
		dep := LocalDir(ip)
		if dep == "" || !rule.Violates(dep) {
			continue
		}
		if _, ok := rule.Allowed[archrules.EdgeKey(dir, dep)]; ok {
			continue
		}
		pass.Reportf(spec.Pos(),
			"package %s imports %s, which layering rule %q forbids (packages under %v must not import "+
				"packages under %v). If this is a deliberate, reviewed exception, add %q to that rule's "+
				"Allowed map in archrules.LayeringRules naming the fix required to remove it.",
			dir, ip, rule.Name, rule.From, rule.Forbid, archrules.EdgeKey(dir, dep))
	}
}

// reportStaleEdges reports each Allowed entry keyed to dir that no longer
// exempts a real violation. A stale exception is worse than none: left in
// place it silently covers whatever import lands on that edge next.
func reportStaleEdges(pass *analysis.Pass, rule archrules.LayeringRule, dir string, imported map[string]bool) {
	for key, why := range rule.Allowed {
		from, dep, ok := strings.Cut(key, " -> ")
		if !ok || from != dir {
			continue
		}
		if reason := staleEdgeReason(rule, dir, dep, imported); reason != "" {
			pass.Reportf(pass.Files[0].Package, "layering rule %q allows %q (%s) but %s", rule.Name, key, why, reason)
		}
	}
}

// staleEdgeReason says why the edge dir -> dep no longer needs an exemption,
// or "" when it still does.
func staleEdgeReason(rule archrules.LayeringRule, dir, dep string, imported map[string]bool) string {
	switch {
	case !rule.MatchesFrom(dir):
		return fmt.Sprintf("%q is not under the rule's From prefixes %v — delete the entry", dir, rule.From)
	case !rule.Violates(dep):
		return fmt.Sprintf("%q is not something the rule forbids — delete the entry", dep)
	case !imported[dep]:
		return fmt.Sprintf("%s no longer imports %s — delete the entry, or it will "+
			"silently exempt that edge when it comes back", dir, dep)
	}
	return ""
}
