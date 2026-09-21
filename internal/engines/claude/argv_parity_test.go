package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
	// env and cwd are the rest of what the launch execs: the merged child
	// environment (ExecuteEnv) and the working directory Setup recorded.
	env map[string]string
	cwd string
	// perm, servers, runEnv and presented are the launch's inputs as the
	// port hands them to Instance.Exec: the posture, the delivered MCP
	// server names, the run env and the surfaces' presentations.
	perm      agent.PermissionMode
	servers   []string
	runEnv    map[string]string
	presented []present.Presentation
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
	// normalise rewrites every temp root in s to its placeholder.
	normalise := func(roots map[string]string, s string) string {
		for ph, real := range roots {
			s = strings.ReplaceAll(s, real, ph)
		}
		return s
	}
	var lines []argvLine
	for _, l := range launches {
		for _, mode := range []agent.ExecutionMode{agent.ModeInteractive, agent.ModeOneshot} {
			for _, perm := range []agent.PermissionMode{agent.PermissionDefault, agent.PermissionPlan, agent.PermissionBypass, agent.PermissionAcceptEdits} {
				for _, model := range []string{"", "claude-opus-5"} {
					env := map[string]string{sessionHarpEnv: "perky-same-chevy"}
					if home, ok := l.roots["<HOME>"]; ok {
						env[ConfigDirEnv] = home // a relocated home rides the run env, as the launch sets it
					}
					req := &agent.ExecuteRequest{
						Mode:        mode,
						Permissions: perm,
						Model:       model,
						Env:         env,
						Prompt:      &agent.Fragment{Content: "reply with PROMPTOK"},
					}
					args := l.backend.buildArgs(req)
					for i, a := range args {
						args[i] = normalise(l.roots, a)
					}
					execEnv := map[string]string{}
					for k, v := range l.backend.ExecuteEnv(req) {
						execEnv[k] = normalise(l.roots, v)
					}
					presented := l.backend.presented()
					for i := range presented {
						for j := range presented[i].Args {
							presented[i].Args[j] = normalise(l.roots, presented[i].Args[j])
						}
					}
					runEnv := map[string]string{}
					for k, v := range env {
						runEnv[k] = normalise(l.roots, v)
					}
					lines = append(lines, argvLine{
						key: fmt.Sprintf("%s/%s/%s/model=%q", l.name, mode, perm, model), mode: mode, args: args,
						env: execEnv, cwd: normalise(l.roots, l.backend.WorkDir()),
						perm: perm, servers: mcpServerNames(l.backend.Resolved()), runEnv: runEnv, presented: presented,
					})
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

// goldenCompare compares got against testdata/<name>; a missing golden is
// captured and the run fails naming the capture (regenerate only for a
// deliberate change, by deleting the golden).
func goldenCompare(t *testing.T, name, got, drifted string) {
	t.Helper()
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	golden := filepath.Join(dir, "testdata", name)
	want, err := os.ReadFile(golden)
	if os.IsNotExist(err) {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		t.Fatalf("captured %s; re-run to compare against it", golden)
	}
	require.NoError(t, err)
	require.Equal(t, string(want), got, drifted)
}

// envLine renders an env map deterministically: sorted k=v pairs.
func envLine(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, " ")
}

// TestExec_LaunchParity_Golden is the anti-drift pin for the engine INSTANCE
// extraction: for the same 64-launch matrix, the argv, the merged child env
// and the working directory a launch execs are byte-identical to
// testdata/exec_parity.golden, captured before Instance.Exec existed. A
// difference here is a STOP for that work, never something to re-capture
// around.
func TestExec_LaunchParity_Golden(t *testing.T) {
	var out strings.Builder
	for _, l := range argvMatrix(t) {
		fmt.Fprintf(&out, "%s:\n  argv: %s\n  env: %s\n  cwd: %s\n", l.key, strings.Join(l.args, " "), envLine(l.env), l.cwd)
	}
	goldenCompare(t, "exec_parity.golden", out.String(), "claude's launch (argv/env/cwd) drifted from the captured golden")
}

// TestChatArgs_Parity_Golden pins the STRUCTURED drive's argv — one
// stream-json turn the runner drives — over posture × model × resume ×
// mcp-config. The golden was first captured from the separate chatArgs
// composition; it holds the Instance's Exec over the runner's projection
// (launch.Launch.Session's shape) plus the driver's protocol: the same
// flags, in Exec's order, since ONE place composes argv.
func TestChatArgs_Parity_Golden(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	var out strings.Builder
	for _, perm := range []agent.PermissionMode{agent.PermissionDefault, agent.PermissionPlan, agent.PermissionBypass, agent.PermissionAcceptEdits} {
		for _, model := range []string{"", "claude-opus-5"} {
			for _, resume := range []string{"", "native-key-1"} {
				for _, mcp := range []string{"", "<HOME>/.mcp.json"} {
					s := engine.Session{
						Identity:   sessions.Identity{Harp: "perky-same-chevy"},
						Label:      engine.LabelConfig{Label: EngineName, Model: model},
						Mode:       engine.Structured,
						Permission: perm,
					}
					var presented []present.Presentation
					if mcp != "" {
						s.MCPServers = []string{"probe"}
						presented = []present.Presentation{{HostPath: mcp, EnginePath: mcp, Args: []string{flagMCPConfig, mcp}}}
					}
					i, err := kind.Instance(s)
					require.NoError(t, err)
					inst := i.(*instance)
					if resume != "" {
						require.NoError(t, inst.Resume(resume))
					}
					ex, err := inst.Exec(presented)
					require.NoError(t, err)
					argv := (&streamJSONDriver{inst: inst}).argv(ex, engine.Turn{})
					fmt.Fprintf(&out, "%s/model=%q/resume=%q/mcp=%q: %s\n", perm, model, resume, mcp, strings.Join(argv, " "))
				}
			}
		}
	}
	goldenCompare(t, "chat_parity.golden", out.String(), "claude's structured argv drifted from the captured golden")
}
