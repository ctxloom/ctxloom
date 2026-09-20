package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// hostStart advises a project root and a session home on the host.
func hostStart(t *testing.T) (present.Start, string, string) {
	t.Helper()
	project, home := t.TempDir(), t.TempDir()
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project, Engine: project},
		EngineHome:  present.Root{Host: home, Engine: home},
	})), project, home
}

func claudeDef(t *testing.T) engine.Base {
	t.Helper()
	e, err := Build()
	require.NoError(t, err)
	return e.Root()
}

func TestBuild_DeclaresEverySurface_HooksIncluded_AndBothHalves(t *testing.T) {
	def := claudeDef(t)
	require.Equal(t, engine.Name(EngineName), def.Name)
	require.Equal(t, []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills}, def.Static())
	require.NotNil(t, def.Dynamic, "claude provides the dynamic half")
	require.True(t, def.Permissions.ReadOnlyPlan)
	require.Equal(t, engine.PermissionBypass, def.Permissions.HostDefault)
	require.Equal(t, engine.DistributionDefault, def.Distribution)
}

// TestBuild_GrammarIsTheEngineCLIProjection: Definition.CLI is derived from
// the L1 EngineCLI declarations, one grammar per mode, and every argv of the
// parity matrix — what claude actually composes — parses against its mode's
// grammar. This is the extraction's anti-drift test in its minimal form;
// the port's own (Exec parses against Definition.CLI) lands with Instance.
func TestBuild_GrammarIsTheEngineCLIProjection(t *testing.T) {
	def := claudeDef(t)
	for _, l := range argvMatrix(t) {
		g, ok := engine.CLIFor(def.CLI, engine.Mode(l.mode))
		require.True(t, ok, "%s: no grammar for %v", l.key, l.mode)
		require.Equal(t, "claude", g.Binary)
		_, err := g.ParseArgv(l.args)
		require.NoError(t, err, "%s: argv the engine composed is refused by its own grammar", l.key)
	}
	interactive, _ := engine.CLIFor(def.CLI, engine.Interactive)
	_, err := interactive.ParseArgv([]string{"--no-such-flag"})
	require.ErrorIs(t, err, engine.ErrArgv)
}

func TestDeliverContext_WritesTheFramedPromptUnderTheSessionHome(t *testing.T) {
	def := claudeDef(t)
	start, _, home := hostStart(t)
	d, err := def.Context.DeliverContext(start, present.RootSessionHome, engine.ContextInputs{Text: []byte("project rules")}, nil)
	require.NoError(t, err)
	require.Len(t, d.Wrote, 1)
	require.True(t, strings.HasPrefix(d.Wrote[0], home+string(filepath.Separator)), "landed under the session home: %s", d.Wrote[0])
	body, err := os.ReadFile(d.Wrote[0])
	require.NoError(t, err)
	require.Contains(t, string(body), "project rules")
	require.Equal(t, flagAppendSystemFile, d.Presented.Args[0], "announced on the system-prompt flag")
	_, err = def.Context.DeliverContext(start, present.RootProjectRoot, engine.ContextInputs{}, nil)
	require.Error(t, err, "a root the approach does not offer is refused")
}

func TestDeliverMCP_WritesTheServerSetUnderTheSelectedRoot(t *testing.T) {
	def := claudeDef(t)
	start, project, home := hostStart(t)
	in := engine.MCPInputs{Servers: map[string]wire.MCPServer{"probe": {Command: "probe-mcp"}}}
	for root, dir := range map[present.RootKind]string{present.RootSessionHome: home, present.RootProjectRoot: project} {
		_, err := def.MCP.DeliverMCP(start, root, in, nil)
		require.NoError(t, err)
		body, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
		require.NoError(t, err, "root %v", root)
		require.Contains(t, string(body), "probe-mcp")
	}
}

// TestDeliverHooks_RegistersTheHookInSettings is the ruling's proof for the
// definition: Hooks is a surface of its own, and delivering it writes a real
// hook registration into claude's native form — the hooks section of
// settings.json — through the settings writer. Settings delivers its own
// part (statusline, the deny list) into the same file. The two kinds fold
// into ONE managed block of one file, which the writer re-manages on every
// write: composing both into a single write is the delivery router's
// (delivery.Route) when it plans claude's settings file, not this seam's.
func TestDeliverHooks_RegistersTheHookInSettings(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	hooks := wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}}}
	_, err := def.Hooks.DeliverHooks(start, present.RootProjectRoot, engine.HooksInputs{Hooks: hooks}, nil)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(body), "session-bind")
}

func TestDeliverSettings_WritesStatuslineAndDenyList(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	_, err := def.Settings.DeliverSettings(start, present.RootProjectRoot, engine.SettingsInputs{DenyTools: []string{"Task"}, Statusline: true}, nil)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(body), `"Task"`)
	require.Contains(t, string(body), "statusLine")
}

func TestDeliverCommandsAndSkills_LandUnderTheProjectRoot(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	_, err := def.Commands.DeliverCommands(start, present.RootProjectRoot, engine.CommandsInputs{Commands: []engine.CommandExport{{Name: "greet", Body: []byte("say hi"), Enabled: true, Description: "greets"}}}, nil)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(project, ".claude", "commands", "greet.md"))
	require.NoError(t, err)
	require.Contains(t, string(body), "say hi")
	require.Contains(t, string(body), "greets")

	_, err = def.Skills.DeliverSkills(start, present.RootProjectRoot, engine.SkillsInputs{Skills: []engine.SkillExport{{Name: "greet", Description: "greets", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("---\nname: greet\ndescription: g\n---\nbody")}}}}}, nil)
	require.NoError(t, err)
	body, err = os.ReadFile(filepath.Join(project, ".claude", "skills", "greet", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(body), "body")
}

func TestDynamic_NamesTheSessionEndpointWithItsBearer(t *testing.T) {
	def := claudeDef(t)
	entry := def.Dynamic.Endpoint(sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: "tok"})
	require.Equal(t, "http://127.0.0.1:1/mcp", entry.URL)
	require.Equal(t, "Bearer tok", entry.Headers["Authorization"])
}

// TestDeclaration_IsDerivedFromTheDefinition: the named-form table today's
// launch path reads is a projection of the typed approaches' Forms, kind by
// kind — one table.
func TestDeclaration_IsDerivedFromTheDefinition(t *testing.T) {
	decl := Declaration()
	for _, kind := range claudeDef(t).Static() {
		if kind == present.Hooks {
			continue // hooks ride the settings forms; no named form of their own
		}
		require.NotEmpty(t, decl.Names(kind), "kind %v has no runtime forms", kind)
	}
	require.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachSystemPrompt, agent.ApproachHook}, decl.Names(agent.SurfaceContext))
	def, _ := decl.Default(agent.SurfaceMCP)
	require.Equal(t, ApproachMCPConfig, def)
}
