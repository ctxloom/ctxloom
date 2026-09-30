package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// argvLine is one launch of the parity matrix: its key and the argv
// claude composed, with temp roots normalised to placeholders.
type argvLine struct {
	key  string
	mode agent.ExecutionMode
	args []string
}

// argvMatrix composes the argv for the fixed launch matrix: a launch whose
// runner delivered every out-of-cwd surface (the three flag-carrying
// presentations), one that delivered the MCP config alone, and a bare
// launch × interactive/oneshot × every posture × with/without a model. The
// presentations are what the runner hands Execute (ExecuteRequest.Presented)
// after its static writer delivered the launch's package.
func argvMatrix(t *testing.T) []argvLine {
	t.Helper()
	home := t.TempDir()
	flag := func(name, file string) present.Presentation {
		path := filepath.Join(home, file)
		return present.Presentation{HostPath: path, EnginePath: path, Args: []string{name, path}}
	}
	type launch struct {
		name      string
		presented []present.Presentation
	}
	launches := []launch{
		{"delivered", []present.Presentation{
			flag(flagAppendSystemFile, "abc123.sysprompt.md"),
			flag(flagMCPConfig, ".mcp.json"),
			flag(flagSettings, "settings.json"),
		}},
		{"mcp-only", []present.Presentation{flag(flagMCPConfig, ".mcp.json")}},
		{"bare", nil},
	}
	backend := NewClaudeCode()
	var lines []argvLine
	for _, l := range launches {
		for _, mode := range []agent.ExecutionMode{agent.ModeInteractive, agent.ModeOneshot} {
			for _, perm := range []agent.PermissionMode{agent.PermissionDefault, agent.PermissionPlan, agent.PermissionBypass, agent.PermissionAcceptEdits, agent.PermissionDontAsk, agent.PermissionAuto} {
				for _, model := range []string{"", "claude-opus-5"} {
					req := &agent.ExecuteRequest{
						Mode:        mode,
						Permissions: perm,
						Model:       model,
						Env:         map[string]string{sessionHarpEnv: "perky-same-chevy"},
						Prompt:      &agent.Fragment{Content: "reply with PROMPTOK"},
						Presented:   l.presented,
					}
					args := backend.buildArgs(req)
					for i, a := range args {
						args[i] = strings.ReplaceAll(a, home, "<HOME>")
					}
					lines = append(lines, argvLine{key: fmt.Sprintf("%s/%s/%s/model=%q", l.name, mode, perm, model), mode: mode, args: args})
				}
			}
		}
	}
	return lines
}

// TestBuildArgs_ArgvParity_Golden is the anti-drift pin for claude's argv:
// what the Instance composes for the fixed matrix is byte-identical to
// testdata/argv_parity.golden. Regenerate only for a deliberate argv change,
// by deleting the golden and re-running (a missing golden is captured and
// the run fails naming the capture).
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
	for _, perm := range []agent.PermissionMode{agent.PermissionDefault, agent.PermissionPlan, agent.PermissionBypass, agent.PermissionAcceptEdits, agent.PermissionDontAsk, agent.PermissionAuto} {
		for _, model := range []string{"", "claude-opus-5"} {
			for _, resume := range []string{"", "native-key-1"} {
				for _, mcp := range []string{"", "<HOME>/.mcp.json"} {
					s := engine.Session{
						Identity:   sessions.Identity{Harp: "perky-same-chevy"},
						Label:      engine.LabelConfig{Label: EngineName, Model: model},
						Mode:       engine.Structured,
						Permission: engine.PermissionPolicy{Mode: perm},
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
					// The posture the runner hands a first turn: the launch's mode,
					// bypass excepted (it stays on the argv).
					posture := engine.TurnPosture{Mode: perm}
					if perm == agent.PermissionBypass {
						posture.Mode = engine.PermissionNotRequested
					}
					argv, err := (&streamJSONDriver{inst: inst}).argv(ex, engine.Turn{Posture: posture})
					require.NoError(t, err)
					fmt.Fprintf(&out, "%s/model=%q/resume=%q/mcp=%q: %s\n", perm, model, resume, mcp, strings.Join(argv, " "))
				}
			}
		}
	}
	goldenCompare(t, "chat_parity.golden", out.String(), "claude's structured argv drifted from the captured golden")
}
