package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// claude implements the tool-less marker (marauding-magnolia, decision #5):
// the compile-time half of the proof.
var _ engine.ToolLess = Claude{}

// toolLessPresented are what a delivery could present: an MCP file on
// --mcp-config (a tool-less launch's package carries no server, but the
// instance does not rely on it) and the framed context.
var toolLessPresented = []present.Presentation{
	{Args: []string{flagMCPConfig, "/h/.mcp.json"}},
	{Args: []string{flagAppendSystemFile, "/h/system.md"}},
}

// toolLessInstance binds a structured tool-less session at trust. The
// session's own posture says plan with a grant-bearing approver — the
// tool-less launch must not read it.
func toolLessInstance(t *testing.T, trust engine.WorkspaceTrust) *instance {
	t.Helper()
	kind, err := Build()
	require.NoError(t, err)
	tl, ok := kind.(engine.ToolLess)
	require.True(t, ok, "Build's kind implements the marker")
	inst, err := tl.ToolLess(engine.Session{
		Identity: sessions.Identity{Harp: "h"}, Label: engine.LabelConfig{Label: EngineName, Model: "haiku"},
		Mode: engine.Structured, Permission: modePolicy(modePlan), Prompt: "p", Trust: trust,
		Roots: present.Paths{ProjectRoot: present.Root{Host: "/host/p", Engine: "/p"}},
	})
	require.NoError(t, err)
	return inst.(*instance)
}

// The exact argv claude's tool-less launch emits: no built-in tool
// (--tools ""), no MCP server (--strict-mcp-config, and no --mcp-config),
// and nobody asked (--permission-prompts none) — on a trusted repository
// as on an untrusted one, whose sources were already user-only.
func TestToolLess_ExecArgv(t *testing.T) {
	for name, tc := range map[string]struct {
		trust engine.WorkspaceTrust
		want  []string
	}{
		"untrusted": {engine.TrustUntrusted, []string{
			flagPermissionPrompts, "none", flagTools, "", flagModel, "haiku", flagPrint,
			flagSettingSources, "user", flagStrictMCPConfig, flagAppendSystemFile, "/h/system.md",
		}},
		"trusted": {engine.TrustTrusted, []string{
			flagPermissionPrompts, "none", flagTools, "", flagModel, "haiku", flagPrint,
			flagStrictMCPConfig, flagAppendSystemFile, "/h/system.md",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			ex, err := toolLessInstance(t, tc.trust).Exec(toolLessPresented)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ex.Args)
			assert.Equal(t, []byte("p"), ex.StdinPrompt)

			g, ok := engine.CLIFor(claudeDef(t).CLI, engine.Structured)
			require.True(t, ok)
			parsed, err := g.ParseArgv(ex.Args)
			require.NoError(t, err, "the tool-less argv parses against claude's own grammar")
			tools, ok := parsed.Flags[flagTools]
			assert.True(t, ok)
			assert.Empty(t, tools, "--tools carries an EMPTY value token: no built-in tool")
		})
	}
}

// Every structured turn of a tool-less session is the same launch: its
// --settings say only dontAsk, whatever posture or grants the turn carries.
func TestToolLess_TurnArgv(t *testing.T) {
	inst := toolLessInstance(t, engine.TrustUntrusted)
	ex, err := inst.Exec(toolLessPresented)
	require.NoError(t, err)
	for name, in := range map[string]engine.Turn{
		"no posture":     {Prompt: "p"},
		"a turn posture": {Prompt: "p", Posture: engine.TurnPosture{Mode: modeAcceptEdits, Grants: []string{"Bash(ls)"}}},
	} {
		t.Run(name, func(t *testing.T) {
			args, err := inst.turnArgv(ex, in)
			require.NoError(t, err)
			want := append(append([]string{}, ex.Args...),
				flagInputFormat, "stream-json", flagOutputFormat, "stream-json", flagVerbose,
				flagName, "h", flagSettings, `{"permissions":{"defaultMode":"dontAsk"}}`)
			assert.Equal(t, want, args)
		})
	}
}
