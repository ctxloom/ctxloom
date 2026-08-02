package shell_test

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/frontend/shell"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// lowerCall's `expandConfig(&sc)` — taking the address of a
// local that is later returned BY VALUE — could be read as a coupling hazard. It is not a live
// defect: every write through that pointer happens during the expansion calls
// that lowerCall makes before its own `return sc`, so the copy carries them.
// The aliasing is what DELIVERS the substitution capture, which is why the
// hazard cannot be removed by "just don't alias": the pin below is red for any
// rewrite that gives the expander a config bound to something other than the
// command being returned.

// TestExpansionCaptureSurvivesTheReturnedCopy pins the behaviour the aliasing
// exists to produce: command and process substitutions found while expanding a
// command's words must be present on the SimpleCommand the lowerer hands back,
// so rules see the commands hidden inside them.
func TestExpansionCaptureSurvivesTheReturnedCopy(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"argument substitution", `echo $(git push --force)`, "git"},
		{"assignment value", `X=$(git push --force) echo hi`, "git"},
		{"redirect target", `echo hi > $(git push --force)`, "git"},
		{"process substitution", `diff <(git push --force) b`, "git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := shell.New().Parse(context.Background(), ir.ShellBash, tc.src)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.src, err)
			}
			var progs []string
			for _, c := range s.Commands() {
				progs = append(progs, c.Program())
			}
			found := false
			for _, p := range progs {
				if p == tc.want {
					found = true
				}
			}
			if !found {
				t.Errorf("Parse(%q) programs = %v; the nested %q was lost, so the expander wrote into a command that was not returned", tc.src, progs, tc.want)
			}
		})
	}
}
