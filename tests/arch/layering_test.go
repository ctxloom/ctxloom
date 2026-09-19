//go:build arch

// The layering gate: every rule in archrules.LayeringRules, enforced over the
// module's production imports by the same scan() arch_test.go uses. The rules
// themselves — what may import what, and the reasoned, edge-keyed
// exceptions — are declared once, in archrules, and read by this gate and by
// archlint's LayeringAnalyzer alike. Adding a rule is an entry there; nothing
// here changes.
//
// What stays test-side is what a per-package analyzer cannot see: that every
// prefix a rule names still matches a real package, and that an allowlisted
// edge's from-package still exists at all.
package arch

import (
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// TestArch_LayeringRules is the general layering gate: for every rule in
// archrules.LayeringRules, no non-test file in a from-subtree package may import a
// forbidden-subtree package unless that directory is named in the rule's
// Allowed map. Depending on genuinely shared packages outside both subtrees
// (config, paths, clifmt, shared/*) is unaffected — only imports that
// resolve under `forbid` are checked.
func TestArch_LayeringRules(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range archrules.LayeringRules {
		rule := rule
		t.Run(rule.Name, func(t *testing.T) {
			dirs := make([]string, 0, len(pkgs))
			for dir := range pkgs {
				if rule.MatchesFrom(dir) {
					dirs = append(dirs, dir)
				}
			}
			sort.Strings(dirs)
			if len(dirs) == 0 {
				t.Fatalf("the scan found no package under %v — rule %q is looking at the wrong tree", rule.From, rule.Name)
			}
			// Every prefix the rule names must match a real package: a
			// from/forbid/except entry that matches nothing is a path that
			// moved out from under the rule, and it would silently stop
			// guarding (or stop excepting) whatever it used to name.
			for _, group := range [][]string{rule.From, rule.Forbid, rule.Except} {
				for _, prefix := range group {
					if !anyPackageUnder(pkgs, prefix) {
						t.Errorf("rule %q names prefix %q, which matches no package in this module — delete or re-point it", rule.Name, prefix)
					}
				}
			}

			for _, dir := range dirs {
				for _, ip := range pkgs[dir].imports {
					dep := localDir(ip)
					if dep == "" || !rule.Violates(dep) {
						continue
					}
					if why, ok := rule.Allowed[archrules.EdgeKey(dir, dep)]; ok {
						t.Logf("allowed: %s imports %s (%s)", dir, ip, why)
						continue
					}
					t.Errorf("package %s imports %s, which layering rule %q forbids (packages under %v must not "+
						"import packages under %v). If this is a deliberate, reviewed exception, add %q to that "+
						"rule's Allowed map in archrules.LayeringRules naming the fix required to remove it.",
						dir, ip, rule.Name, rule.From, rule.Forbid, archrules.EdgeKey(dir, dep))
				}
			}
		})
	}
}

// anyPackageUnder reports whether the scan found a package at prefix or in
// its subtree.
func anyPackageUnder(pkgs map[string]*pkg, prefix string) bool {
	for dir := range pkgs {
		if archrules.UnderAny(dir, []string{prefix}) {
			return true
		}
	}
	return false
}

// TestArch_LayeringAllowlist_IsLive fails when a LayeringRule's Allowed map
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

	for _, rule := range archrules.LayeringRules {
		keys := make([]string, 0, len(rule.Allowed))
		for k := range rule.Allowed {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			from, dep, ok := strings.Cut(key, " -> ")
			if !ok {
				t.Errorf("rule %q allows %q, which is not an edge key (\"<from dir> -> <dep dir>\")", rule.Name, key)
				continue
			}
			p, ok := pkgs[from]
			switch {
			case !ok:
				t.Errorf("rule %q allows %q, but %q is not a package in this module — delete the entry", rule.Name, key, from)
				continue
			case !rule.MatchesFrom(from):
				t.Errorf("rule %q allows %q, but %q is not under the rule's from-prefixes %v — delete the entry", rule.Name, key, from, rule.From)
				continue
			case !rule.Violates(dep):
				t.Errorf("rule %q allows %q, but %q is not something the rule forbids — delete the entry", rule.Name, key, dep)
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
					"exempt that edge when it comes back", rule.Name, key, rule.Allowed[key], from, dep)
			}
		}
	}
}
