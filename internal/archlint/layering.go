package archlint

import (
	"golang.org/x/tools/go/analysis"
)

// layeringRule states that no production file in a package under From may
// import a package under any subtree in Forbid. Allowed is the rule's
// shrinking allowlist: a From-subtree directory that violates the rule today,
// mapped to the fix required to remove it.
type layeringRule struct {
	Name    string
	From    string
	Forbid  []string
	Allowed map[string]string
}

// layeringRules is the table both the cycle-prevention rules and the
// operations/cli boundary live in. Adding a rule is an entry here; nothing
// else changes.
//
// Each cycle-prevention rule pins a relationship that is only NOT an import
// cycle because one direction lives entirely in an external _test package.
// The compiler refuses a real cycle, but only once BOTH edges exist in
// production code — so the change that reverses a direction gets no signal,
// and the later, innocent change gets the error. These rules move the signal
// to the reversal.
var layeringRules = []layeringRule{
	{
		Name:   "coord-must-not-import-cli/tui",
		From:   "internal/agentcoord/coord",
		Forbid: []string{"internal/cli/tui"},
	},
	{
		Name:   "termui-must-not-import-cli/tui",
		From:   "internal/termui",
		Forbid: []string{"internal/cli/tui"},
	},
	{
		Name:   "transcript-must-not-import-lm/grpc",
		From:   "internal/transcript",
		Forbid: []string{"internal/lm/grpc"},
	},
	{
		Name:   "shared/agent-must-not-import-engine-plugins",
		From:   "internal/shared/agent",
		Forbid: []string{"internal/claude", "internal/codex", "internal/kiro"},
	},
	{
		// internal/operations is the frontend-agnostic layer both the CLI and
		// MCP call into, so it must never import back up into internal/cli.
		Name:   "operations-must-not-import-cli",
		From:   "internal/operations",
		Forbid: []string{"internal/cli"},
	},
	{
		// pkg/clifmt is the CLI output layer, and it SHIPS AS A STANDALONE
		// LIBRARY independent of ctxloom (ruled 2026-08-22). It sits at the
		// outermost edge of the hexagon: adapters import it, it imports
		// nothing of ours. Depending on cobra or any other external module is
		// fine and is NOT checked here -- LocalDir returns "" for anything
		// outside this repo, so only in-repo imports reach this rule. The
		// property holds today (clifmt has zero non-test ctxloom imports);
		// this rule is what keeps it true through the canonical-struct
		// redesign that is about to touch this package.
		Name:   "clifmt-must-not-import-ctxloom",
		From:   "pkg/clifmt",
		Forbid: []string{"internal", "cmd"},
	},
	{
		// operations is the ports-and-adapters core: it may depend only on the
		// injected, polymorphic internal/lm/backends seam, never on a concrete
		// engine package, so backend identity cannot be branched on directly.
		Name:   "operations-must-not-import-engine-plugins",
		From:   "internal/operations",
		Forbid: []string{"internal/claude", "internal/codex", "internal/kiro", "internal/opencode", "internal/acp"},
	},
}

func (r layeringRule) forbids(dep string) bool {
	for _, f := range r.Forbid {
		if UnderSubtree(dep, f) {
			return true
		}
	}
	return false
}

// LayeringAnalyzer enforces the layering table: no production file in a
// From-subtree package may import a Forbid-subtree package unless the
// directory is named in that rule's allowlist. Depending on genuinely shared
// packages outside both subtrees is unaffected — only imports resolving under
// Forbid are checked.
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

	for _, rule := range layeringRules {
		if !UnderSubtree(dir, rule.From) {
			continue
		}
		violates := false
		for ip, spec := range imports {
			dep := LocalDir(ip)
			if dep == "" || !rule.forbids(dep) {
				continue
			}
			violates = true
			if _, ok := rule.Allowed[dir]; ok {
				continue
			}
			pass.Reportf(spec.Pos(),
				"package %s imports %s, which layering rule %q forbids (packages under %q must not import "+
					"packages under %v). If this is a deliberate, reviewed exception, add %q to that rule's "+
					"Allowed map in internal/archlint/layering.go naming the fix required to remove it.",
				dir, ip, rule.Name, rule.From, rule.Forbid, dir)
		}
		// A stale exception is worse than none: left in place it silently
		// covers whatever import lands in that directory next.
		if why, ok := rule.Allowed[dir]; ok && !violates && allowlistLivenessEnabled() {
			pass.Reportf(pass.Files[0].Package,
				"layering rule %q allows %q (%s) but it no longer imports anything the rule forbids — "+
					"delete the entry", rule.Name, dir, why)
		}
	}
	return nil, nil
}
