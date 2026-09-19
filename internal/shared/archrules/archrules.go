// Package archrules is the ONE declaration of ctxloom's architectural rule
// tables. Two runners enforce them — the TestArch_ gates in tests/arch and
// the go/analysis analyzers in internal/shared/archlint (the pre-commit hook)
// — and both read the tables from here. A table copied into each runner
// drifts: one side gains a row the other never sees, and the stale copy keeps
// enforcing while lying. TestArch_RuleTables_DeclaredOnce fails on a second
// declaration.
//
// This package is a LEAF: it imports nothing of ours, so both a test package
// and an analyzer can import it without crossing a layer the tables
// themselves forbid. Keep it that way; a rule table that depends on the code
// it governs cannot be read by a runner that sits outside that code.
//
// What lives here is DATA and the matching that gives it meaning. How a
// violation is found — parsing, type-checking, position reporting — is each
// runner's own; the anti-vacuity floors (“the sweep saw at least N files”)
// stay test-side because an analyzer can never learn how many packages exist.
package archrules

import "strings"

// UnderAny reports whether dir is one of the prefixes or inside its subtree.
// Rules are written against subtrees, so "internal/adapters/cli" covers
// "internal/adapters/cli/tui" while never matching a sibling like
// "internal/adapters/clifmt".
func UnderAny(dir string, prefixes []string) bool {
	for _, p := range prefixes {
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}
