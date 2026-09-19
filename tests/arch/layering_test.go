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
//	internal/claude            -> internal/shared/agent      (production)
//	internal/shared/agent_test -> internal/claude            (test-only)
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

// layeringRule states that no non-test file in a package under any prefix in
// `from` (the directory itself or any subdirectory) may import a package under
// any prefix in `forbid` (same subtree matching) unless that package is under
// a prefix in `except`. `except` exists for the ring-shaped rules: "core may
// import only core and the toolbox" is `forbid: every in-repo root, except:
// the core and toolbox prefixes`, which forbids a NEW package by default (the
// conservative direction) instead of leaving it unforbidden until someone
// lists it. `allowed` is this rule's shrinking allowlist, keyed by EDGE —
// "<from dir> -> <dep dir>" — mapped to the fix required to remove it, in
// the same spirit as arch_test.go's testSupportImporters and
// internal/cli's formatDebtAllowlist. An edge key rather than a package key
// is what makes the ratchet fine-grained: a package with five forbidden
// imports has five entries, each deleted (by TestArch_LayeringAllowlist_IsLive)
// the moment its own import leaves, instead of one entry that stays live —
// and keeps masking the other four — until the last of them goes. A
// nil/empty map means the rule holds with zero exceptions.
type layeringRule struct {
	name    string
	from    []string
	forbid  []string
	except  []string
	allowed map[string]string
}

// edgeKey is the allowed-map key for one import edge.
func edgeKey(from, dep string) string { return from + " -> " + dep }

// layeringRules is the one table both T11's cycle-prevention rules and
// T18's layering rule live in. Add a rule here; nothing else in this file
// needs to change.
var layeringRules = []layeringRule{
	{
		name:   "coord-must-not-import-cli/tui",
		from:   []string{"internal/agentcoord/coord"},
		forbid: []string{"internal/cli/tui"},
	},
	{
		name:   "termui-must-not-import-cli/tui",
		from:   []string{"internal/termui"},
		forbid: []string{"internal/cli/tui"},
	},
	{
		name:   "transcript-must-not-import-lm/grpc",
		from:   []string{"internal/transcript"},
		forbid: []string{"internal/lm/grpc"},
	},
	{
		name:   "shared/agent-must-not-import-engine-plugins",
		from:   []string{"internal/shared/agent"},
		forbid: []string{"internal/claude"},
	},
	{
		// The coarse ancestor of T20's future `cli/<flow> -> operations/<flow>
		// -> domain` rule: internal/operations is the frontend-agnostic layer
		// both the CLI and MCP call into, so it must never import back up
		// into internal/cli. When the per-flow split lands, this entry can
		// be replaced by one per flow (`internal/operations/<flow>` must not
		// import `internal/cli/<other-flow>`) without touching the mechanism.
		name:   "operations-must-not-import-cli",
		from:   []string{"internal/operations"},
		forbid: []string{"internal/cli"},
	},
	{
		// THE DECIDED ARCHITECTURE'S CORE RING (docs/architecture/audit-2026-09-18/
		// 30-decided-architecture.md, Part 1.0): the packages that become
		// internal/core/* import only each other and the toolbox. Until the
		// rename slice makes the rings directories, `from` and `except` name
		// today's paths one by one; after it, each collapses to a prefix.
		// `forbid` is every in-repo root, so anything that is neither core
		// nor toolbox is forbidden by default — a new package needs no row.
		// The allowlist is the MEASURED import list, one edge per entry,
		// each naming the slice in which it leaves; it is a ratchet, not a
		// claim (Part 0, invariant 9), and TestArch_LayeringAllowlist_IsLive
		// deletes an entry the moment its import is gone.
		name: "core-imports-only-core",
		from: []string{
			"internal/trust",
			"internal/sessions",
			"internal/profiles",
			"internal/bundles",
			"internal/config",
			"internal/paths",
			"internal/shared/wire",
			"internal/shared/agent",
			"internal/agentcoord/spool",
			"internal/agentcoord/coord",
			"internal/lm/engine",
		},
		forbid: []string{"cmd", "container", "internal", "pkg", "resources", "scripts"},
		except: []string{
			// core (the from-set again: core may import core)
			"internal/trust",
			"internal/sessions",
			"internal/profiles",
			"internal/bundles",
			"internal/config",
			"internal/paths",
			"internal/shared/wire",
			"internal/shared/agent",
			"internal/agentcoord/spool",
			"internal/agentcoord/coord",
			"internal/lm/engine",
			// the toolbox (Part 0: domain-free leaf libraries)
			"internal/shared/iox",
			"internal/shared/lockwait",
			"internal/shared/collections",
			"internal/shared/keymatch",
			"internal/shared/textutil",
			"internal/shared/yamlx",
			"internal/shared/realpath",
			"internal/shared/harp",
			"internal/shared/pidalive",
			"internal/errs",
			"internal/refuri",
			"internal/schema",
			"internal/liveness",
		},
		allowed: map[string]string{},
	},
	{
		// pkg/clifmt is the CLI output layer and SHIPS AS A STANDALONE
		// LIBRARY independent of ctxloom (ruled 2026-08-22). It is the
		// outermost edge: adapters import it, it imports nothing of ours.
		// External modules (cobra, and whatever a consumer brings) are
		// deliberately unchecked -- localDir returns "" for anything outside
		// this repo, so only in-repo imports reach this rule.
		name:   "clifmt-must-not-import-ctxloom",
		from:   []string{"pkg/clifmt"},
		forbid: []string{"internal", "cmd"},
	},
}

// underAny reports whether dir is one of the prefixes or inside its subtree.
func underAny(dir string, prefixes []string) bool {
	for _, p := range prefixes {
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}

// matchesFrom reports whether dir is inside a from-subtree (or is one
// itself), and separately whether an import path resolves under any of the
// rule's forbidden subtrees; excepted whether it is nonetheless permitted by
// the rule's except list; violates composes the two.
func (r layeringRule) matchesFrom(dir string) bool { return underAny(dir, r.from) }

func (r layeringRule) matchesForbidden(dep string) bool {
	for _, f := range r.forbid {
		if dep == f || strings.HasPrefix(dep, f+"/") {
			return true
		}
	}
	return false
}

func (r layeringRule) excepted(dep string) bool { return underAny(dep, r.except) }

func (r layeringRule) violates(dep string) bool { return r.matchesForbidden(dep) && !r.excepted(dep) }

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
				t.Fatalf("the scan found no package under %v — rule %q is looking at the wrong tree", rule.from, rule.name)
			}
			// Every prefix the rule names must match a real package: a
			// from/forbid/except entry that matches nothing is a path that
			// moved out from under the rule, and it would silently stop
			// guarding (or stop excepting) whatever it used to name.
			for _, group := range [][]string{rule.from, rule.forbid, rule.except} {
				for _, prefix := range group {
					if !anyPackageUnder(pkgs, prefix) {
						t.Errorf("rule %q names prefix %q, which matches no package in this module — delete or re-point it", rule.name, prefix)
					}
				}
			}

			for _, dir := range dirs {
				for _, ip := range pkgs[dir].imports {
					dep := localDir(ip)
					if dep == "" || !rule.violates(dep) {
						continue
					}
					if why, ok := rule.allowed[edgeKey(dir, dep)]; ok {
						t.Logf("allowed: %s imports %s (%s)", dir, ip, why)
						continue
					}
					t.Errorf("package %s imports %s, which layering rule %q forbids (packages under %v must not "+
						"import packages under %v). If this is a deliberate, reviewed exception, add %q to that "+
						"rule's allowed map in tests/arch/layering_test.go naming the fix required to remove it.",
						dir, ip, rule.name, rule.from, rule.forbid, edgeKey(dir, dep))
				}
			}
		})
	}
}

// anyPackageUnder reports whether the scan found a package at prefix or in
// its subtree.
func anyPackageUnder(pkgs map[string]*pkg, prefix string) bool {
	for dir := range pkgs {
		if dir == prefix || strings.HasPrefix(dir, prefix+"/") {
			return true
		}
	}
	return false
}

// TestArch_LayeringAllowlist_IsLive fails when a layeringRule's allowed map
// names an edge that no longer exists — the from-package is gone, is not
// under the rule's from-prefixes, no longer imports the dep, or the dep is no
// longer forbidden — the same staleness check
// TestArch_TestSupportAllowlist_IsLive runs for testSupportImporters. A
// stale exception is worse than none: left in place, it would silently
// cover whatever a later, unrelated import lands on that edge. This is the
// ratchet Part 1.0 of the decided architecture names: an allowlist entry is
// deleted in the slice that removes its import, and this test is what says
// so.
func TestArch_LayeringAllowlist_IsLive(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range layeringRules {
		keys := make([]string, 0, len(rule.allowed))
		for k := range rule.allowed {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			from, dep, ok := strings.Cut(key, " -> ")
			if !ok {
				t.Errorf("rule %q allows %q, which is not an edge key (\"<from dir> -> <dep dir>\")", rule.name, key)
				continue
			}
			p, ok := pkgs[from]
			switch {
			case !ok:
				t.Errorf("rule %q allows %q, but %q is not a package in this module — delete the entry", rule.name, key, from)
				continue
			case !rule.matchesFrom(from):
				t.Errorf("rule %q allows %q, but %q is not under the rule's from-prefixes %v — delete the entry", rule.name, key, from, rule.from)
				continue
			case !rule.violates(dep):
				t.Errorf("rule %q allows %q, but %q is not something the rule forbids — delete the entry", rule.name, key, dep)
				continue
			}
			stillImports := false
			for _, ip := range p.imports {
				if localDir(ip) == dep {
					stillImports = true
					break
				}
			}
			if !stillImports {
				t.Errorf("rule %q allows %q (%s) but %s no longer imports %s — delete the entry, or it will silently "+
					"exempt that edge when it comes back", rule.name, key, rule.allowed[key], from, dep)
			}
		}
	}
}
