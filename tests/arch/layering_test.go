//go:build arch

// The layering rules' CORPUS half. The rules themselves — what may import
// what, and the reasoned, edge-keyed exceptions — are declared once, in
// archrules.LayeringRules, and enforced by archlint's LayeringAnalyzer, which
// also fails an allowlisted edge its package no longer has.
//
// What stays here is what a per-package analyzer cannot see, because it needs
// the whole module at once: that every prefix a rule names still matches a
// real package, and that an allowlisted edge's from-package still exists at
// all. A pass is never handed a package that does not exist, so an entry
// naming one is invisible to the analyzer.
package arch

import (
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// TestArch_LayeringRules fails when a rule names a prefix that matches no
// package in the module. A from/forbid/except entry that matches nothing is a
// path that moved out from under the rule: it silently stops guarding (or
// stops excepting) whatever it used to name, and the analyzer — which only
// ever asks "is THIS package under the prefix" — reports nothing.
func TestArch_LayeringRules(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range archrules.LayeringRules {
		t.Run(rule.Name, func(t *testing.T) {
			for _, group := range [][]string{rule.From, rule.Forbid, rule.Except} {
				for _, prefix := range group {
					if !anyPackageUnder(pkgs, prefix) {
						t.Errorf("rule %q names prefix %q, which matches no package in this module — delete or re-point it", rule.Name, prefix)
					}
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
// holds a key that is not an edge, or an edge whose from-package no longer
// exists. A stale exception is worse than none: left in place, it would
// silently cover whatever later takes that package's path and import. The
// other staleness shapes — the edge's import is gone, the from-package left
// the rule's From, the dep is no longer forbidden — are the analyzer's.
func TestArch_LayeringAllowlist_IsLive(t *testing.T) {
	pkgs := scan(t)

	for _, rule := range archrules.LayeringRules {
		keys := make([]string, 0, len(rule.Allowed))
		for k := range rule.Allowed {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			from, _, ok := strings.Cut(key, " -> ")
			if !ok {
				t.Errorf("rule %q allows %q, which is not an edge key (\"<from dir> -> <dep dir>\")", rule.Name, key)
				continue
			}
			if _, ok := pkgs[from]; !ok {
				t.Errorf("rule %q allows %q, but %q is not a package in this module — delete the entry", rule.Name, key, from)
			}
		}
	}
}
