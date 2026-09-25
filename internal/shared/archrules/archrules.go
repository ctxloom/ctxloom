// Package archrules is the ONE declaration of ctxloom's architectural rule
// tables. The analyzers in internal/shared/archlint enforce them, and the
// tests/arch gates that need the whole module at once read them too; every
// reader takes the table from here. A table copied into a reader drifts: one
// copy gains a row the other never sees, and the stale copy keeps enforcing
// while lying. TestArch_RuleTables_DeclaredOnce fails on a second declaration.
//
// This package is a LEAF: it imports nothing of ours, so both a test package
// and an analyzer can import it without crossing a layer the tables
// themselves forbid. Keep it that way; a rule table that depends on the code
// it governs cannot be read by a reader that sits outside that code.
//
// What lives here is DATA and the matching that gives it meaning. How a
// violation is found — parsing, type-checking, position reporting — is each
// reader's own; the anti-vacuity floors (“the sweep saw at least N files”)
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
