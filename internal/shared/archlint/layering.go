package archlint

import (
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
	Run:  runLayering,
}

func runLayering(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" {
		return nil, nil
	}
	imports := ImportPaths(pass)
	imported := map[string]bool{}
	for ip := range imports {
		if dep := LocalDir(ip); dep != "" {
			imported[dep] = true
		}
	}

	for _, rule := range archrules.LayeringRules {
		if rule.MatchesFrom(dir) {
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
		if !allowlistLivenessEnabled() {
			continue
		}
		// A stale exception is worse than none: left in place it silently
		// covers whatever import lands on that edge next.
		for key, why := range rule.Allowed {
			from, dep, ok := strings.Cut(key, " -> ")
			if !ok || from != dir {
				continue
			}
			switch {
			case !rule.MatchesFrom(dir):
				pass.Reportf(pass.Files[0].Package,
					"layering rule %q allows %q (%s) but %q is not under the rule's From prefixes %v — delete the entry",
					rule.Name, key, why, dir, rule.From)
			case !rule.Violates(dep):
				pass.Reportf(pass.Files[0].Package,
					"layering rule %q allows %q (%s) but %q is not something the rule forbids — delete the entry",
					rule.Name, key, why, dep)
			case !imported[dep]:
				pass.Reportf(pass.Files[0].Package,
					"layering rule %q allows %q (%s) but %s no longer imports %s — delete the entry, or it will "+
						"silently exempt that edge when it comes back", rule.Name, key, why, dir, dep)
			}
		}
	}
	return nil, nil
}
