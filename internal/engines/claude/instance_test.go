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
		Permission: engine.PermissionPlan,
		MCPServers: []string{"probe"},
	}
	inst, err := kind.Instance(s)
	require.NoError(t, err)
	mcp := present.Presentation{HostPath: "/h/.mcp.json", EnginePath: "/h/.mcp.json", Args: []string{flagMCPConfig, "/h/.mcp.json"}}
	ex, err := inst.Exec([]present.Presentation{mcp})
	require.NoError(t, err)
	require.Equal(t, "--permission-mode plan --disallowedTools Bash,Edit,Write,NotebookEdit --allowedTools mcp__probe --model claude-opus-5 --print --mcp-config /h/.mcp.json", strings.Join(ex.Args, " "))
	drivers := inst.Drivers()
	require.Len(t, drivers, 1)
	d, ok := drivers[0].(*streamJSONDriver)
	require.True(t, ok)
	require.Equal(t, strings.Join(ex.Args, " ")+" --input-format stream-json --output-format stream-json --verbose --resume native-key-1 --name perky-same-chevy",
		strings.Join(d.argv(ex, engine.Turn{Prompt: "hi", Resume: "native-key-1"}), " "))
	require.NoError(t, inst.Resume("native-key-2"))
	ex, err = inst.Exec([]present.Presentation{mcp})
	require.NoError(t, err)
	require.Contains(t, strings.Join(ex.Args, " "), "--resume native-key-2", "a resumed instance continues its native session on the next Exec")
	require.Equal(t, strings.Count(strings.Join(d.argv(ex, engine.Turn{Prompt: "hi", Resume: "native-key-2"}), " "), "--resume"), 1, "the driver adds no second --resume")
}

// TestInstance_ExecParsesAgainstOwnGrammar: the argv Exec composes, for every
// mode, parses against the grammar the Definition declares for that mode.
func TestInstance_ExecParsesAgainstOwnGrammar(t *testing.T) {
	kind, err := Build()
	require.NoError(t, err)
	for _, mode := range kind.Root().Modes {
		s := engine.Session{Identity: sessions.Identity{Harp: "h"}, Label: engine.LabelConfig{Label: EngineName}, Mode: mode, Permission: engine.PermissionBypass, Prompt: "p"}
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
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionBypass, Model: "m", Env: map[string]string{sessionHarpEnv: "h"}, Prompt: &agent.Fragment{Content: "p"}}
	ex, err := b.exec(req)
	require.NoError(t, err)
	require.Equal(t, ex.Args, b.buildArgs(req))
	merged := maps.Clone(req.Env)
	maps.Copy(merged, ex.Env)
	require.Equal(t, fmt.Sprint(merged), fmt.Sprint(b.ExecuteEnv(req)), "the launch env is the request's with Exec's engine-native vars over it")
}
