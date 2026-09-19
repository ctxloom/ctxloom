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
func TestBuildArgs_ArgvParity_Golden(t *testing.T) {
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
	var out strings.Builder
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
					line := strings.Join(args, " ")
					for ph, real := range l.roots {
						line = strings.ReplaceAll(line, real, ph)
					}
					fmt.Fprintf(&out, "%s/%s/%s/model=%q: %s\n", l.name, mode, perm, model, line)
				}
			}
		}
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
