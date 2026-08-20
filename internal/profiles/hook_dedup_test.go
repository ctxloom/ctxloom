package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func preToolOf(h wire.HooksConfig) []string {
	out := make([]string, 0, len(h.Unified.PreTool))
	for _, x := range h.Unified.PreTool {
		out = append(out, x.Command)
	}
	return out
}

// TestResolveProfile_SharedAncestorHookRunsOnce pins the diamond: a hook a
// shared ancestor declares must reach the resolved profile ONCE, however many
// inheritance paths reach that ancestor.
//
// MEASURED before the fix: child parents [b, c], both parenting d, d declaring
// one pre_tool hook, resolved to ["shared-hook" "shared-hook"] — the command
// ran twice per event. The resolver memoizes so d RESOLVES once, but its
// resolved RESULT was merged along both paths.
func TestResolveProfile_SharedAncestorHookRunsOnce(t *testing.T) {
	dir := t.TempDir()
	w := func(name, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644))
	}
	w("d", "hooks:\n  unified:\n    pre_tool:\n      - command: shared-hook\n        type: command\n")
	w("b", "parents:\n  - d\n")
	w("c", "parents:\n  - d\n")
	w("child", "parents:\n  - b\n  - c\n")

	r, err := NewLoader([]string{dir}).ResolveProfile("child", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"shared-hook"}, preToolOf(r.Hooks),
		"a shared ancestor's hook reaches the child once, not once per path")
}

// The control: dedup is by the hook's whole content SCOPED TO THE EVENT, so
// genuinely different hooks all survive and the same command on two lifecycles
// stays two hooks. Without this, "runs once" would also be satisfied by a merge
// that dropped hooks it should have kept.
func TestResolveProfile_DistinctHooksAllSurvive(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(
		"hooks:\n  unified:\n    pre_tool:\n"+
			"      - command: one\n        type: command\n"+
			"      - command: two\n        type: command\n"+
			"      - command: one\n        type: command\n        matcher: Bash\n"+
			"    session_start:\n      - command: one\n        type: command\n",
	), 0o644))

	r, err := NewLoader([]string{dir}).ResolveProfile("p", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two", "one"}, preToolOf(r.Hooks),
		"a different matcher is a different hook: only an exact repeat collapses")
	require.Len(t, r.Hooks.Unified.SessionStart, 1,
		"the same command on another lifecycle is its own hook, not a duplicate of the pre_tool one")
}
