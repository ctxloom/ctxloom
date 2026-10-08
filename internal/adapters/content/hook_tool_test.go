package content

import (
	"context"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// A tree hook narrows by a neutral tool class and never by an engine's own
// tool names. The content file decodes leniently, so the refusals are
// explicit: a dropped `matcher:` would leave the hook firing on every tool.
//
// MUTATION -- drop the RefusedMatcher check (or the Known check) from
// hookType.Decode -- turns this red.
func TestHook_TreeHookNarrowsByToolClassOnly(t *testing.T) {
	cases := []struct {
		label, body, wantErr string
		want                 wire.ToolClass
	}{
		{"a tool class", "tool: shell\ntype: command\ncommand: guard\n", "", wire.ToolShell},
		{"an engine-native matcher", "matcher: Bash\ntype: command\ncommand: guard\n", "matcher", ""},
		{"an unknown class", "tool: Bash\ntype: command\ncommand: guard\n", `"Bash"`, ""},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			store := fixtureStore(t)
			writeFile(t, store.fsys, fixtureRoot+"/code-quality/hooks/pre_tool/guard.yaml", c.body)
			surf, err := mustHookItem(t, store, "code-quality", "pre_tool/guard").Surface(context.Background())
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("Surface error = %v, want one naming %s", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Surface: %v", err)
			}
			if got := surf.(Hook).Tool; got != c.want {
				t.Fatalf("Tool = %q, want %q", got, c.want)
			}
		})
	}
}
