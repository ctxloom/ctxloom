package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// TestBuildArgs_ArgvParity_Golden is the anti-drift pin for the engine
// definition extraction: the argv claude composes for a fixed matrix of
// launches is byte-identical to testdata/argv_parity.golden, captured
// before the declaration table moved onto the Definition. Temp roots are
// normalised to placeholders so the capture is stable. A difference here is
// a STOP for that work, never something to re-capture around; regenerate
// only for a deliberate argv change, by deleting the golden and re-running
// (a missing golden is captured and the run fails naming the capture).
// argvLine is one launch of the parity matrix: its key and the argv
// claude composed, with temp roots normalised to placeholders.
type argvLine struct {
	key  string
	mode agent.ExecutionMode
	args []string
}

// argvMatrix composes the argv for the fixed launch matrix: shared,
// isolated, minimal and bare Setups × interactive/oneshot × four postures ×
// with/without a model.
func argvMatrix(t *testing.T) []argvLine {
	t.Helper()
	work := t.TempDir()
	managed := &agent.ManagedConfig{
		ManageStatusline: true,
		DenyTools:        []string{"Task"},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
		}},
		BundleMCP: map[string]wire.MCPServer{"probe": {Command: "probe-mcp"}},
	}
	type launch struct {
		name    string
		backend *ClaudeCode
		roots   map[string]string // placeholder → real prefix
	}
	shared, sharedRoots := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)
	isolated, isolatedHome := setupClaudeIsolated(t, work, managed)
	launches := []launch{
		{"shared", shared, map[string]string{"<EPHEM>": sharedRoots.ephem, "<HOME>": sharedRoots.home, "<WORK>": work}},
		{"isolated", isolated, map[string]string{"<HOME>": isolatedHome, "<WORK>": work}},
		{"minimal", minimalBackend(t, "claude-sonnet-5"), nil},
		{"bare", NewClaudeCode(), nil},
	}
	var lines []argvLine
	for _, l := range launches {
		for _, mode := range []agent.ExecutionMode{agent.ModeInteractive, agent.ModeOneshot} {
			for _, perm := range []agent.PermissionMode{agent.PermissionDefault, agent.PermissionPlan, agent.PermissionBypass, agent.PermissionAcceptEdits} {
				for _, model := range []string{"", "claude-opus-5"} {
					req := &agent.ExecuteRequest{
						Mode:        mode,
						Permissions: perm,
						Model:       model,
						Env:         map[string]string{sessionHarpEnv: "perky-same-chevy"},
						Prompt:      &agent.Fragment{Content: "reply with PROMPTOK"},
					}
					args := l.backend.buildArgs(req)
					for i, a := range args {
						for ph, real := range l.roots {
							args[i] = strings.ReplaceAll(a, real, ph)
							a = args[i]
						}
					}
					lines = append(lines, argvLine{key: fmt.Sprintf("%s/%s/%s/model=%q", l.name, mode, perm, model), mode: mode, args: args})
				}
			}
		}
	}
	return lines
}

// TestBuildArgs_ArgvParity_Golden is the anti-drift pin for the engine
// definition extraction: the argv claude composes for the fixed matrix is
// byte-identical to testdata/argv_parity.golden, captured before the
// declaration table moved onto the Definition. A difference here is a STOP
// for that work, never something to re-capture around; regenerate only for
// a deliberate argv change, by deleting the golden and re-running (a
// missing golden is captured and the run fails naming the capture).
func TestBuildArgs_ArgvParity_Golden(t *testing.T) {
	var out strings.Builder
	for _, l := range argvMatrix(t) {
		fmt.Fprintf(&out, "%s: %s\n", l.key, strings.Join(l.args, " "))
	}
	// TestMain runs the binary in a throwaway sandbox cwd, so the golden is
	// located from this file's own directory, as the other fixtures are.
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	golden := filepath.Join(dir, "testdata", "argv_parity.golden")
	want, err := os.ReadFile(golden)
	if os.IsNotExist(err) {
		require.NoError(t, os.WriteFile(golden, []byte(out.String()), 0o644))
		t.Fatalf("captured %s; re-run to compare against it", golden)
	}
	require.NoError(t, err)
	require.Equal(t, string(want), out.String(), "claude's argv drifted from the captured golden")
}
