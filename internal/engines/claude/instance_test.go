package claude

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// TestInstance_StructuredDriver_ArgvIsExecPlusTheProtocol pins the
// structured drive's argv: the Exec the Instance composed, then the
// stream-json protocol flags, the resume key the turn names and the harp
// the session is named after (testdata/chat_parity.golden holds the same
// flags; the ORDER is now Exec's, since one place composes argv).
func TestInstance_StructuredDriver_ArgvIsExecPlusTheProtocol(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	s := engine.Session{
		Identity:   sessions.Identity{Harp: "perky-same-chevy"},
		Label:      engine.LabelConfig{Label: EngineName, Model: "claude-opus-5"},
		Mode:       engine.Structured,
		Permission: modePolicy(modePlan),
		MCPServers: []string{"probe"},
	}
	inst, err := kind.Instance(s)
	require.NoError(t, err)
	mcp := present.Presentation{HostPath: "/h/.mcp.json", EnginePath: "/h/.mcp.json", Args: []string{flagMCPConfig, "/h/.mcp.json"}}
	ex, err := inst.Exec([]present.Presentation{mcp})
	require.NoError(t, err)
	require.Equal(t, "--disallowedTools Bash,Edit,Write,NotebookEdit --allowedTools mcp__probe --permission-prompts none --model claude-opus-5 --print --mcp-config /h/.mcp.json", strings.Join(ex.Args, " "))
	drivers := inst.Drivers()
	require.Len(t, drivers, 1)
	d, ok := drivers[0].(*streamJSONDriver)
	require.True(t, ok)
	argv, err := d.argv(ex, engine.Turn{Prompt: "hi", Resume: "native-key-1", Posture: engine.TurnPosture{Mode: modePlan}})
	require.NoError(t, err)
	require.Equal(t, strings.Join(ex.Args, " ")+` --input-format stream-json --output-format stream-json --verbose --resume native-key-1 --name perky-same-chevy --settings {"permissions":{"defaultMode":"plan"}}`,
		strings.Join(argv, " "))
	require.NoError(t, inst.Resume("native-key-2"))
	ex, err = inst.Exec([]present.Presentation{mcp})
	require.NoError(t, err)
	require.Contains(t, strings.Join(ex.Args, " "), "--resume native-key-2", "a resumed instance continues its native session on the next Exec")
	argv, err = d.argv(ex, engine.Turn{Prompt: "hi", Resume: "native-key-2"})
	require.NoError(t, err)
	require.Equal(t, strings.Count(strings.Join(argv, " "), "--resume"), 1, "the driver adds no second --resume")
}

// TestInstance_ExecPinsClassicScreen: an interactive launch carries the
// classic-renderer pin and declares it on its surface; a print run has no TUI
// and carries neither.
func TestInstance_ExecPinsClassicScreen(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	for _, mode := range kind.Root().Modes {
		s := engine.Session{Identity: sessions.Identity{Harp: "h"}, Label: engine.LabelConfig{Label: EngineName}, Mode: mode, Permission: modePolicy(modeBypass), Prompt: "p"}
		inst, err := kind.Instance(s)
		require.NoError(t, err)
		ex, err := inst.Exec(nil)
		require.NoError(t, err)
		if mode == engine.Interactive {
			require.Equal(t, "1", ex.Env[classicScreenEnv], "%v", mode)
			continue
		}
		require.NotContains(t, ex.Env, classicScreenEnv, "%v", mode)
	}
	for _, cli := range ClaudeEngineCLIs() {
		if cli.Surface == agent.CLISurfaceInteractive {
			require.Contains(t, cli.SetEnv, classicScreenEnv, "%v", cli.Surface)
			continue
		}
		require.NotContains(t, cli.SetEnv, classicScreenEnv, "%v", cli.Surface)
	}
}

// TestInstance_HeadlessChildDefaults: a structured (headless) run has no human
// at the engine, so it says so to claude — --permission-prompts none: deny
// what the posture and rules leave open instead of asking nobody — and turns
// off background tasks, which would otherwise outlive the per-turn process
// and answer into a turn nobody is reading. An interactive run has a human
// and carries neither. Both are declared on the surface they ride.
func TestInstance_HeadlessChildDefaults(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	for _, mode := range kind.Root().Modes {
		for _, named := range namedPolicies {
			perm := named.name
			s := engine.Session{Identity: sessions.Identity{Harp: "h"}, Label: engine.LabelConfig{Label: EngineName}, Mode: mode, Permission: named.p, Prompt: "p"}
			inst, err := kind.Instance(s)
			require.NoError(t, err)
			ex, err := inst.Exec(nil)
			require.NoError(t, err)
			argv := strings.Join(ex.Args, " ")
			if mode == engine.Structured {
				require.Contains(t, argv, flagPermissionPrompts+" none", "%v %v", mode, perm)
				require.Equal(t, "1", ex.Env[disableBackgroundTasksEnv], "%v %v", mode, perm)
				continue
			}
			require.NotContains(t, argv, flagPermissionPrompts, "%v %v", mode, perm)
			require.NotContains(t, ex.Env, disableBackgroundTasksEnv, "%v %v", mode, perm)
		}
	}
	for _, cli := range ClaudeEngineCLIs() {
		if cli.Surface == agent.CLISurfaceOneshot {
			require.Contains(t, cli.SetEnv, disableBackgroundTasksEnv)
			continue
		}
		require.NotContains(t, cli.SetEnv, disableBackgroundTasksEnv, "%v", cli.Surface)
	}
}

// TestInstance_ExecParsesAgainstOwnGrammar: the argv Exec composes, for every
// mode, parses against the grammar the Definition declares for that mode.
func TestInstance_ExecParsesAgainstOwnGrammar(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	for _, mode := range kind.Root().Modes {
		s := engine.Session{Identity: sessions.Identity{Harp: "h"}, Label: engine.LabelConfig{Label: EngineName}, Mode: mode, Permission: modePolicy(modeBypass), Prompt: "p"}
		inst, err := kind.Instance(s)
		require.NoError(t, err)
		ex, err := inst.Exec(nil)
		require.NoError(t, err)
		g, ok := engine.CLIFor(kind.Root().CLI, mode)
		require.True(t, ok)
		_, err = g.ParseArgv(ex.Args)
		require.NoError(t, err, "%v: %s", mode, strings.Join(ex.Args, " "))
	}
}

// TestBuildArgs_IsInstanceExec: buildArgs no longer composes anything — it
// is the request projected onto a Session and the resolved surfaces onto
// presentations, handed to Instance.Exec. The golden proves parity; this
// pins the delegation so a second composition cannot reappear.
func TestBuildArgs_IsInstanceExec(t *testing.T) {
	b := NewClaudeCode()
	req := withSession(b, &agent.ExecuteRequest{Mode: agent.ModeInteractive, Model: "m", Env: map[string]string{sessionHarpEnv: "h"}, Prompt: &agent.Fragment{Content: "p"}}, modePolicy(modeBypass))
	ex, err := b.exec(req)
	require.NoError(t, err)
	require.Equal(t, ex.Args, b.buildArgs(req))
	b.SetExecuteEnv(func(*agent.ExecuteRequest) map[string]string { return ex.Env }) // as Execute registers it
	merged := maps.Clone(req.Env)
	maps.Copy(merged, ex.Env)
	require.Equal(t, fmt.Sprint(merged), fmt.Sprint(b.ExecuteEnv(req)), "the launch env is the request's with Exec's engine-native vars over it")
}
