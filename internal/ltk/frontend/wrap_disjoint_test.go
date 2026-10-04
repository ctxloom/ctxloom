package frontend

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/shellenv"
)

// TestWrapperTablesAreDisjoint pins the invariant prefixWrapperRules' doc
// states: no program is both an interpreter wrapper (its inner command is a
// string to re-parse) and an argv-prepending wrapper (its inner command is
// argv to strip). A program in both tables is expanded twice, under two
// incompatible readings of its arguments. The check goes through
// wrapperRule.matches with the dialect shellenv resolves, which is how
// expandWrappers itself decides, so an overlap by shell dialect is caught as
// well as one by explicit program name.
func TestWrapperTablesAreDisjoint(t *testing.T) {
	for _, prefix := range prefixWrapperRules {
		for _, prog := range prefix.programs {
			sh := shellenv.ShellFromPath(prog)
			for i, rule := range wrapperRules {
				if rule.matches(prog, sh) {
					t.Errorf("program %q is in prefixWrapperRules and also matches wrapperRules[%d]; the tables must be disjoint", prog, i)
				}
			}
		}
	}
}
