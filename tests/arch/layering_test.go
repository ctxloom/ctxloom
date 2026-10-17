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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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

// TestArch_LayeringForbidModules_NameRealModules fails when a rule's
// ForbidModules prefix matches no module this one requires. A prefix that
// names nothing guards nothing: a typo'd or renamed toolkit path would leave
// the rule green while the toolkit it meant to keep out is imported freely.
func TestArch_LayeringForbidModules_NameRealModules(t *testing.T) {
	required := requiredModules(t)
	for _, rule := range archrules.LayeringRules {
		for _, prefix := range rule.ForbidModules {
			found := false
			for _, mod := range required {
				if archrules.UnderAny(mod, []string{prefix}) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("rule %q forbids module prefix %q, which matches no module go.mod requires — delete or re-point it", rule.Name, prefix)
			}
		}
	}
}

// requiredModules is every module path go.mod requires, direct or indirect:
// the first field of each line of a require block, and of each single-line
// require.
func requiredModules(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "go.mod"))
	require.NoError(t, err)
	var out []string
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
		case inBlock && fields[0] == ")":
			inBlock = false
		case inBlock:
			out = append(out, fields[0])
		case fields[0] == "require" && len(fields) >= 2 && fields[1] == "(":
			inBlock = true
		case fields[0] == "require" && len(fields) >= 3:
			out = append(out, fields[1])
		}
	}
	require.NotEmpty(t, out, "go.mod requires nothing — the gate is reading the wrong file")
	return out
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

// TestArch_EnginePluginRules_ForbidEveryEngine: the rules keeping core/agent
// and operations off concrete engines forbid the WHOLE engines ring — every
// engine package the scan finds, plus kit and the registry root — not one
// engine by name. A rule spelling a single engine's path guards only that
// engine, and a newly added engine would be imported unseen.
func TestArch_EnginePluginRules_ForbidEveryEngine(t *testing.T) {
	pkgs := scan(t)
	var engines []string
	for dir := range pkgs {
		if archrules.UnderAny(dir, []string{"internal/engines"}) {
			engines = append(engines, dir)
		}
	}
	sort.Strings(engines)
	require.Contains(t, engines, "internal/engines/mock", "the scan must see more than one engine for this test to mean anything")

	for _, name := range []string{"shared/agent-must-not-import-engine-plugins", "operations-must-not-import-engine-plugins"} {
		var rule *archrules.LayeringRule
		for i := range archrules.LayeringRules {
			if archrules.LayeringRules[i].Name == name {
				rule = &archrules.LayeringRules[i]
			}
		}
		require.NotNil(t, rule, "layering rule %q is gone", name)
		for _, dir := range engines {
			require.True(t, rule.Violates(dir), "rule %q does not forbid %s", name, dir)
		}
	}
}
