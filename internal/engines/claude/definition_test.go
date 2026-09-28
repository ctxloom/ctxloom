package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/engine/conformance"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// hostStart advises a project root and a session home on the host.
func hostStart(t *testing.T) (present.Start, string, string) {
	t.Helper()
	project, home := t.TempDir(), t.TempDir()
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project, Engine: project},
		SessionHome: present.Root{Host: home, Engine: home},
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
	require.Equal(t, engine.PermissionAcceptEdits, def.Permissions.HostDefault, "the bare-host default auto-approves edits and prompts for the rest; bypass is only ever declared")
	require.Contains(t, def.Permissions.HostDefaultReason, "acceptEdits")
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
	require.Len(t, d.Presented.Args, 2, "the flag needs a value: the written file")
	require.Equal(t, d.Wrote[0], d.Presented.Args[1], "the flag must name the file Deliver actually wrote")
	require.NotEqual(t, home, d.Presented.Args[1], "the flag must never name the bare private-root directory")
	_, err = def.Context.DeliverContext(start, present.RootWorkDir, engine.ContextInputs{}, nil)
	require.Error(t, err, "a root the approach does not offer is refused")
}

// TestDeliverContext_AtTheProjectRoot_AppendsToCLAUDEmd: the at-rest form
// is the well-known file, the context appended after whatever the user
// already wrote; the ownership record, not a marker, owns the write.
func TestDeliverContext_AtTheProjectRoot_AppendsToCLAUDEmd(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	path := filepath.Join(project, ContextFileName)
	require.NoError(t, os.WriteFile(path, []byte("# theirs\n"), 0o644))
	d, err := def.Context.DeliverContext(start, present.RootProjectRoot, engine.ContextInputs{Text: []byte("project rules")}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{path}, d.Wrote)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# theirs\n\nproject rules\n", string(body))
	require.NotContains(t, string(body), "ctxloom:context", "no marker section")
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
// settings.json. Settings delivers its own part (statusline, the deny
// list) into the same file; each kind ADDS its entries and removes
// nothing, and the ownership record that owns the file takes them back
// out (deliverSettingsFile).
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
	e, err := Build()
	require.NoError(t, err)
	decl := e.(Claude).Declaration()
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

// TestBuild_EverySurfaceDefaultsToTheSessionHome pins the ruling: no
// default anywhere names the project root or the engine's real home. Each
// static approach's FIRST root — the one Route takes when the binding
// selects none — is the session home; the project root is offered second,
// reached only by a binding's `roots:` selection.
func TestBuild_EverySurfaceDefaultsToTheSessionHome(t *testing.T) {
	def := claudeDef(t)
	for kind, a := range def.Surfaces() {
		roots := a.Traits().Roots
		require.NotEmpty(t, roots, "kind %v declares no root", kind)
		require.Equal(t, present.RootSessionHome, roots[0], "kind %v (%s): the first root is the session home, not %v", kind, a.Name(), roots[0])
		require.True(t, a.Traits().Offers(present.RootProjectRoot), "kind %v (%s): the project root stays selectable on the binding", kind, a.Name())
	}
}

// TestRoute_DefaultBindingPlansOnlySessionHomeRoots is the launch-capture
// form: the plan Resolve carries for a binding that selects no root targets
// the session home for every static kind.
func TestRoute_DefaultBindingPlansOnlySessionHomeRoots(t *testing.T) {
	e, err := Build()
	require.NoError(t, err)
	plan, err := conformance.RouteFor(t, conformance.PackageFixture(t), e)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Static)
	for _, it := range plan.Static {
		require.Equal(t, present.RootSessionHome, it.Root, "kind %v routes through %s under %v", it.Kind, it.Approach, it.Root)
	}
}

// TestDeliverCommandsAndSkills_SessionHomeLandsUnderTheEngineHome: at the
// session-home root, commands and skills land where claude reads its
// user-level ones — <config dir>/commands and <config dir>/skills, the
// config dir being the session's CLAUDE_CONFIG_DIR — and nothing reaches
// the project root.
func TestDeliverCommandsAndSkills_SessionHomeLandsUnderTheEngineHome(t *testing.T) {
	def := claudeDef(t)
	start, project, home := hostStart(t)
	_, err := def.Commands.DeliverCommands(start, present.RootSessionHome, engine.CommandsInputs{Commands: []engine.CommandExport{{Name: "greet", Body: []byte("say hi"), Enabled: true, Description: "greets"}}}, nil)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(home, CommandsDirName, "greet.md"))
	require.NoError(t, err)
	require.Contains(t, string(got), "say hi")
	require.NoFileExists(t, filepath.Join(project, ConfigDirName, CommandsDirName, "greet.md"))

	_, err = def.Skills.DeliverSkills(start, present.RootSessionHome, engine.SkillsInputs{Skills: []engine.SkillExport{{Name: "greet", Description: "greets", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("---\nname: greet\ndescription: g\n---\nbody")}}}}}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(home, SkillsDirName, "greet", "SKILL.md"))
	require.NoFileExists(t, filepath.Join(project, ConfigDirName, SkillsDirName, "greet", "SKILL.md"))
}

// TestDeliverCommandsAndSkills_SessionHomeRefusesAnUnrootedRun: with no
// engine home advised there is no session home to write beneath, and the
// tempting fallback — the user's real ~/.claude — is refused, never taken.
func TestDeliverCommandsAndSkills_SessionHomeRefusesAnUnrootedRun(t *testing.T) {
	testsupport.Isolate(t)
	def := claudeDef(t)
	project := t.TempDir()
	start := present.New(present.OnHost(present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}}))
	_, err := def.Commands.DeliverCommands(start, present.RootSessionHome, engine.CommandsInputs{Commands: []engine.CommandExport{{Name: "greet", Body: []byte("say hi"), Enabled: true}}}, nil)
	require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
	_, err = def.Skills.DeliverSkills(start, present.RootSessionHome, engine.SkillsInputs{Skills: []engine.SkillExport{{Name: "greet", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("x")}}}}}, nil)
	require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
	home, _ := os.UserHomeDir()
	require.NoDirExists(t, filepath.Join(home, ConfigDirName), "the real home is never written")
}
