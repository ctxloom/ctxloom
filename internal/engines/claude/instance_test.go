package claude

import (
	"bufio"
	"fmt"
	"maps"
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

// goldenExec is one launch of exec_parity.golden: what the launch execs.
type goldenExec struct {
	argv string
	env  string
	cwd  string
}

// readExecGolden parses testdata/exec_parity.golden into its launches.
func readExecGolden(t *testing.T) map[string]goldenExec {
	t.Helper()
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	f, err := os.Open(filepath.Join(dir, "testdata", "exec_parity.golden"))
	require.NoError(t, err)
	defer f.Close()
	out := map[string]goldenExec{}
	var key string
	var cur goldenExec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "  argv: "):
			cur.argv = strings.TrimPrefix(line, "  argv: ")
		case strings.HasPrefix(line, "  env: "):
			cur.env = strings.TrimPrefix(line, "  env: ")
		case strings.HasPrefix(line, "  cwd: "):
			cur.cwd = strings.TrimPrefix(line, "  cwd: ")
			out[key] = cur
			cur = goldenExec{}
		default:
			key = strings.TrimSuffix(line, ":")
		}
	}
	require.NoError(t, sc.Err())
	require.Len(t, out, 64, "the golden is the 64-launch matrix")
	return out
}

// TestInstance_Exec_MatchesTheLaunchGolden is the anti-drift proof for the
// INSTANCE half: for every launch of the matrix, the Exec claude's Instance
// composes from the engine-facing Session and the presentations the
// delivered surfaces announced is byte-identical — argv, merged env, cwd —
// to what the launch execs today (testdata/exec_parity.golden, captured
// before Instance existed). buildArgs and ExecuteEnv are what the golden
// pinned; this test proves Exec reproduces them from the port's inputs.
func TestInstance_Exec_MatchesTheLaunchGolden(t *testing.T) {
	golden := readExecGolden(t)
	kind, err := Build()
	require.NoError(t, err)
	for _, l := range argvMatrix(t) {
		want, ok := golden[l.key]
		require.True(t, ok, "golden has no launch %s", l.key)
		s := engine.Session{
			Identity:   sessions.Identity{Harp: "perky-same-chevy"},
			Label:      engine.LabelConfig{Label: EngineName, Model: argValue(l.args, flagModel)},
			Mode:       l.mode,
			Permission: l.perm,
			Prompt:     "reply with PROMPTOK",
			WorkDir:    l.cwd,
			MCPServers: l.servers,
		}
		if home, ok := l.env[ConfigDirEnv]; ok {
			s.Home = []engine.HomeBinding{{Var: ConfigDirEnv, Path: home}}
		}
		inst, err := kind.Instance(s)
		require.NoError(t, err)
		ex, err := inst.Exec(l.presented)
		require.NoError(t, err, l.key)
		require.Equal(t, want.argv, strings.Join(ex.Args, " "), "%s: argv", l.key)
		// The launch env is the run's env with the engine-native variables
		// Exec returns laid over it; the golden pinned the merged result.
		merged := maps.Clone(l.runEnv)
		maps.Copy(merged, ex.Env)
		require.Equal(t, want.env, envLine(merged), "%s: env", l.key)
		require.Equal(t, want.cwd, ex.WorkDir, "%s: cwd", l.key)
		require.Equal(t, l.mode == engine.Interactive, ex.Interactive, "%s: a pty exactly for the interactive mode", l.key)
		require.Equal(t, "claude", ex.Binary)
	}
}

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
	require.Equal(t, fmt.Sprint(ex.Env), fmt.Sprint(b.ExecuteEnv(req)))
}
