//go:build arch

// T18/T11: the missing architecture linter, and the six real
// import-cycle-shaped relationships this repo carries today.
//
// T11 found six pairs of packages that are only NOT a production import
// cycle because one direction of the relationship lives entirely in an
// external `_test` package (`coord_test`, `termui_test`, `transcript_test`,
// `agent_test`) rather than in any non-test file:
//
//	internal/cli/tui        -> internal/agentcoord/coord   (production)
//	internal/agentcoord/coord_test -> internal/cli/tui      (test-only)
//
//	internal/cli/tui        -> internal/termui              (production)
//	internal/termui_test     -> internal/cli/tui             (test-only)
//
//	internal/lm/grpc        -> internal/transcript           (production)
//	internal/transcript_test -> internal/lm/grpc             (test-only)
//
//	internal/{claude,codex,kiro} -> internal/shared/agent    (production)
//	internal/shared/agent_test   -> internal/{claude,codex,kiro} (test-only)
//
// The Go compiler already refuses a real cycle, but only once BOTH edges
// exist in production code — which means the developer who adds the SECOND
// edge gets the signal, and the one who added the first (the actual
// direction-reversing change) got none. `go vet ./...` is clean and none of
// these four production dependencies has a production-file reverse edge
// today, so this is prevention, not repair — the six relationships stay this
// shape rather than closing into a real cycle.
//
// T18 separately found no depguard or architecture linter exists at all.
// Rather than stand up a second mechanism next to it, layeringRules extends
// the same scan()/pkg machinery arch_test.go and
// engine_identity_arch_test.go already use (TestArch_NonTestPackages_
// DoNotImportTestSupport, TestArch_Operations_DoesNotImportEnginePlugins):
// parse production imports once, walk a table of "packages under `from` may
// not import packages under `forbid`" rules, and permit named, reasoned
// exceptions the same way testSupportImporters does. A future rule — in
// particular T20's `cli/<flow> -> operations/<flow> -> domain, never
// cli/<flow> -> cli/<other-flow>` layering, once the per-flow package split
// lands — is one more entry in layeringRules, not a new gate.
// `operationsMustNotImportCLI` below is that rule's coarse ancestor: it is
// expressible today because the operations/cli boundary already exists even
// though the flow subdivision does not.
package arch

import (
	"sort"
	"strings"
	"testing"
)

// layeringRule states that no non-test file in a package under `from` (the
// directory itself or any subdirectory) may import a package under any
// prefix in `forbid` (same subtree matching). `allowed` is this rule's
// shrinking allowlist — a `from`-subtree directory that currently violates
// the rule, mapped to the fix required to remove it, in the same shape as
// arch_test.go's testSupportImporters and
// internal/cli/format_coverage_test.go's formatDebtAllowlist. A nil/empty
// map means the rule holds with zero exceptions.
type layeringRule struct {
	name    string
	from    string
	forbid  []string
	allowed map[string]string
}

// layeringRules is the one table both T11's cycle-prevention rules and
// T18's layering rule live in. Add a rule here; nothing else in this file
// needs to change.
var layeringRules = []layeringRule{
	{
		name:   "coord-must-not-import-cli/tui",
		from:   "internal/agentcoord/coord",
		forbid: []string{"internal/cli/tui"},
	},
	{
		name:   "termui-must-not-import-cli/tui",
		from:   "internal/termui",
		forbid: []string{"internal/cli/tui"},
	},
	{
		name:   "transcript-must-not-import-lm/grpc",
		from:   "internal/transcript",
		forbid: []string{"internal/lm/grpc"},
	},
	{
		name:   "shared/agent-must-not-import-engine-plugins",
		from:   "internal/shared/agent",
		forbid: []string{"internal/claude", "internal/codex", "internal/kiro"},
	},
	{
		// The coarse ancestor of T20's future `cli/<flow> -> operations/<flow>
		// -> domain` rule: internal/operations is the frontend-agnostic layer
		// both the CLI and MCP call into, so it must never import back up
		// into internal/cli. When the per-flow split lands, this entry can
		// be replaced by one per flow (`internal/operations/<flow>` must not
		// import `internal/cli/<other-flow>`) without touching the mechanism.
		name:   "operations-must-not-import-cli",
		from:   "internal/operations",
		forbid: []string{"internal/cli"},
	},
}

// underRule reports whether dir is inside the from-subtree (or is from
// itself), and separately whether an import path resolves under any of the
// rule's forbidden subtrees.
func (r layeringRule) matchesFrom(dir string) bool {
	return dir == r.from || strings.HasPrefix(dir, r.from+"/")
}

func (r layeringRule) matchesForbidden(dep string) bool {
	for _, f := range r.forbid {
		if dep == f || strings.HasPrefix(dep, f+"/") {
			return true
		}
	}
	return false
}

// TestArch_LayeringRules is the general layering gate: for every rule in
// layeringRules, no non-test file in a from-subtree package may import a
// forbidden-subtree package unless that directory is named in the rule's
// allowed map. Depending on genuinely shared packages outside both subtrees
// (config, paths, clifmt, shared/*) is unaffected — only imports that
// resolve under `forbid` are checked.
func TestArch_LayeringRules(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range layeringRules {
		rule := rule
		t.Run(rule.name, func(t *testing.T) {
			dirs := make([]string, 0, len(pkgs))
			for dir := range pkgs {
				if rule.matchesFrom(dir) {
					dirs = append(dirs, dir)
				}
			}
			sort.Strings(dirs)
			if len(dirs) == 0 {
				t.Fatalf("the scan found no package under %q — rule %q is looking at the wrong tree", rule.from, rule.name)
			}

			for _, dir := range dirs {
				for _, ip := range pkgs[dir].imports {
					dep := localDir(ip)
					if dep == "" || !rule.matchesForbidden(dep) {
						continue
					}
					if why, ok := rule.allowed[dir]; ok {
						t.Logf("allowed: %s imports %s (%s)", dir, ip, why)
						continue
					}
					t.Errorf("package %s imports %s, which layering rule %q forbids (packages under %q must not "+
						"import packages under %v). If this is a deliberate, reviewed exception, add %q to that "+
						"rule's allowed map in tests/arch/layering_test.go naming the fix required to remove it.",
						dir, ip, rule.name, rule.from, rule.forbid, dir)
				}
			}
		})
	}
}

// TestArch_LayeringAllowlist_IsLive fails when a layeringRule's allowed map
// names a directory that either does not exist or no longer imports
// anything the rule forbids — the same staleness check
// TestArch_TestSupportAllowlist_IsLive runs for testSupportImporters. A
// stale exception is worse than none: left in place, it would silently
// cover whatever a later, unrelated import lands in that directory.
func TestArch_LayeringAllowlist_IsLive(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range layeringRules {
		dirs := make([]string, 0, len(rule.allowed))
		for dir := range rule.allowed {
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs)

		for _, dir := range dirs {
			p, ok := pkgs[dir]
			if !ok {
				t.Errorf("rule %q allows %q, which is not a package in this module — delete the entry", rule.name, dir)
				continue
			}
			stillViolates := false
			for _, ip := range p.imports {
				if rule.matchesForbidden(localDir(ip)) {
					stillViolates = true
					break
				}
			}
			if !stillViolates {
				t.Errorf("rule %q allows %q (%s) but it no longer imports anything the rule forbids — delete the "+
					"entry, or it will silently exempt whatever import lands there next", rule.name, dir, rule.allowed[dir])
			}
		}
	}
}
