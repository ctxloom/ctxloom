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
	"github.com/ctxloom/ctxloom/internal/core/engine/conformance"
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

// TestDeliverContext_AtTheProjectRoot_ClaimsASectionOfCLAUDEmd: at the
// project root the context is a SECTION appended after whatever the user
// already wrote in CLAUDE.md. The approach claims it and writes nothing; the
// static writer lands it, and the ownership record takes it back out.
func TestDeliverContext_AtTheProjectRoot_ClaimsASectionOfCLAUDEmd(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	path := filepath.Join(project, ContextFileName)
	require.NoError(t, os.WriteFile(path, []byte("# theirs\n"), 0o644))
	d, err := def.Context.DeliverContext(start, present.RootProjectRoot, engine.ContextInputs{Text: []byte("project rules")}, nil)
	require.NoError(t, err)
	require.Equal(t, path, d.Presented.HostPath)
	require.Equal(t, map[string][]present.Claim{path: {{Pointer: present.AppendedSection, Value: []byte("project rules")}}}, d.Claims)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "# theirs\n", string(body), "the approach writes nothing")
}

// mcpClaimsIn is the claims d makes on the .mcp.json under dir, by pointer.
func mcpClaimsIn(t *testing.T, d present.Delivered, dir string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, c := range d.Claims[filepath.Join(dir, MCPFileName)] {
		out[c.Pointer] = c.Value
	}
	return out
}

// TestDeliverMCP_ClaimsEachServerUnderTheSelectedRoot: each server is a
// claim on its own entry in the .mcp.json under the root, so the user's
// entries — and another writer's — are never the approach's to rewrite.
func TestDeliverMCP_ClaimsEachServerUnderTheSelectedRoot(t *testing.T) {
	def := claudeDef(t)
	start, project, home := hostStart(t)
	in := engine.MCPInputs{Servers: map[string]wire.MCPServer{"probe": {Command: "probe-mcp"}}}
	for root, dir := range map[present.RootKind]string{present.RootSessionHome: home, present.RootProjectRoot: project} {
		d, err := def.MCP.DeliverMCP(start, root, in, nil)
		require.NoError(t, err)
		got := mcpClaimsIn(t, d, dir)
		require.Len(t, got, 1, "root %v", root)
		require.Equal(t, "probe-mcp", got["/mcpServers/probe"].(map[string]any)["command"], "root %v", root)
		_, err = os.Stat(filepath.Join(dir, MCPFileName))
		require.True(t, os.IsNotExist(err), "the approach writes nothing (root %v)", root)
	}
}

// TestDeliverMCP_ThePrivateFileExistsEvenWithNoServers: --mcp-config names
// the session-home file whatever the run registers, and claude refuses to
// start against a path that does not exist; the project file is the user's
// and is not conjured.
func TestDeliverMCP_ThePrivateFileExistsEvenWithNoServers(t *testing.T) {
	def := claudeDef(t)
	start, project, home := hostStart(t)
	d, err := def.MCP.DeliverMCP(start, present.RootSessionHome, engine.MCPInputs{}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"/mcpServers": map[string]any{}}, mcpClaimsIn(t, d, home))
	d, err = def.MCP.DeliverMCP(start, present.RootProjectRoot, engine.MCPInputs{}, nil)
	require.NoError(t, err)
	require.Empty(t, mcpClaimsIn(t, d, project))
}

// TestDeliverMCP_AtTheProjectRoot_NoClaimHoldsTheRelayBearer: the project
// .mcp.json is a file teams commit, and the record keeps every claimed value.
// The session entry names the bearer by reference and the value rides
// claude's environment (the presentation's env channel, which Exec copies
// into claude's env); claude expands the reference itself. The private
// session-home file is ctxloom's own and keeps the value.
func TestDeliverMCP_AtTheProjectRoot_NoClaimHoldsTheRelayBearer(t *testing.T) {
	def := claudeDef(t)
	start, project, home := hostStart(t)
	const bearer = "bearer-value-under-test"
	entry := def.Dynamic.Endpoint(sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: bearer})
	in := engine.MCPInputs{Servers: map[string]wire.MCPServer{"ctxloom": entry, "probe": {Command: "probe-mcp"}}}

	d, err := def.MCP.DeliverMCP(start, present.RootProjectRoot, in, nil)
	require.NoError(t, err)
	claimed := fmt.Sprint(mcpClaimsIn(t, d, project))
	require.NotContains(t, claimed, bearer, "a project-file claim holds the relay bearer")
	require.Contains(t, claimed, relayBearerRef, "the entry names the bearer by reference")
	require.Equal(t, map[string]string{EnvRelayBearer: bearer}, d.Presented.Env, "the value rides claude's environment")
	require.Equal(t, bearer, in.Servers["ctxloom"].Env[EnvRelayBearer], "the caller's server set is not rewritten")

	ex, err := (&instance{s: engine.Session{Mode: engine.Structured}}).Exec([]present.Presentation{d.Presented})
	require.NoError(t, err)
	require.Equal(t, bearer, ex.Env[EnvRelayBearer], "claude's process env carries the value the file names")

	d, err = def.MCP.DeliverMCP(start, present.RootSessionHome, in, nil)
	require.NoError(t, err)
	require.Contains(t, fmt.Sprint(mcpClaimsIn(t, d, home)), bearer, "the private session-home file is not rewritten by reference")
	require.Empty(t, d.Presented.Env[EnvRelayBearer])
}

// TestBearerByReference_RefusesTwoDifferentBearers: claude's environment
// holds one value per name, so two entries carrying different bearers
// cannot both be named by one reference.
func TestBearerByReference_RefusesTwoDifferentBearers(t *testing.T) {
	two := map[string]wire.MCPServer{
		"a": {Command: "x", Env: map[string]string{EnvRelayBearer: "one"}},
		"b": {Command: "x", Env: map[string]string{EnvRelayBearer: "two"}},
	}
	_, _, err := bearerByReference(two)
	require.ErrorIs(t, err, errTwoRelayBearers)

	same := map[string]wire.MCPServer{
		"a": {Command: "x", Env: map[string]string{EnvRelayBearer: "one"}},
		"b": {Command: "x", Env: map[string]string{EnvRelayBearer: "one"}},
		"c": {Command: "x", Env: map[string]string{EnvRelayBearer: relayBearerRef}},
	}
	out, env, err := bearerByReference(same)
	require.NoError(t, err)
	require.Equal(t, map[string]string{EnvRelayBearer: "one"}, env)
	for name, srv := range out {
		require.Equal(t, relayBearerRef, srv.Env[EnvRelayBearer], name)
	}
}

// settingsClaimsIn is the claims d makes on the project's settings.json, by
// pointer; an element claim is listed under its value.
func settingsClaimsIn(t *testing.T, d present.Delivered, project string) []present.Claim {
	t.Helper()
	return d.Claims[filepath.Join(project, ConfigDirName, SettingsFileName)]
}

// TestDeliverHooks_ClaimsTheHookInTheGroupItsMatcherSelects is the ruling's
// proof for the definition: Hooks is a surface of its own, and delivering it
// claims a real hook registration in claude's native form — a hook in the
// matcher group of its event in settings.json. Settings claims its own part
// (statusline, the deny list) in the same file; the static writer folds both
// into one write.
func TestDeliverHooks_ClaimsTheHookInTheGroupItsMatcherSelects(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	hooks := wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom", Args: []string{"hook", "session-bind"}, Type: "command"}},
		PreShell:     []wire.Hook{{Command: "ltk", Args: []string{"evaluate"}}, {Command: "ltk", Args: []string{"evaluate"}}},
	}
	d, err := def.Hooks.DeliverHooks(start, present.RootProjectRoot, engine.HooksInputs{Hooks: hooks}, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []present.Claim{
		{Pointer: "/hooks/SessionStart/matcher=/hooks/-", Value: map[string]any{"type": "command", "command": "ctxloom", "args": []any{"hook", "session-bind"}}},
		{Pointer: "/hooks/PreToolUse/matcher=Bash/hooks/-", Value: map[string]any{"type": "command", "command": "ltk", "args": []any{"evaluate"}}},
	}, settingsClaimsIn(t, d, project), "one claim per distinct hook, the shell hook in the Bash group")
	_, err = os.Stat(filepath.Join(project, ConfigDirName, SettingsFileName))
	require.True(t, os.IsNotExist(err), "the approach writes nothing")
}

// TestDeliverSettings_ClaimsTheStatuslineAndEachDeny: the statusline is one
// claimed object and each denied tool an element of permissions.deny.
func TestDeliverSettings_ClaimsTheStatuslineAndEachDeny(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	d, err := def.Settings.DeliverSettings(start, present.RootProjectRoot, engine.SettingsInputs{DenyTools: []string{"Task", "", "Task", "WebFetch"}, Statusline: true}, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []present.Claim{
		{Pointer: "/statusLine", Value: map[string]any{"type": "command", "command": ctxloomStatusLineCommand()}},
		{Pointer: "/permissions/deny/-", Value: "Task"},
		{Pointer: "/permissions/deny/-", Value: "WebFetch"},
	}, settingsClaimsIn(t, d, project))
}

// TestDeliverSettings_LeavesAStatuslineThatIsNotCtxloomsCanonicalOne: a
// statusline the user set — their own program, or the ctxloom binary with
// arguments of their choosing — is theirs, and is not claimed.
func TestDeliverSettings_LeavesAStatuslineThatIsNotCtxloomsCanonicalOne(t *testing.T) {
	def := claudeDef(t)
	for _, cmd := range []string{"my-hud", agent.CtxloomCommand() + " hook hud --theme mine"} {
		start, project, _ := hostStart(t)
		path := filepath.Join(project, ConfigDirName, SettingsFileName)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(`{"statusLine": {"type": "command", "command": "`+cmd+`"}}`), 0o644))
		d, err := def.Settings.DeliverSettings(start, present.RootProjectRoot, engine.SettingsInputs{Statusline: true}, nil)
		require.NoError(t, err)
		require.Empty(t, settingsClaimsIn(t, d, project), cmd)
	}
}

// TestDeliverSettings_ClaimsItsOwnCanonicalStatuslineAgain: the canonical
// command is ctxloom's, so a redelivery restates its claim.
func TestDeliverSettings_ClaimsItsOwnCanonicalStatuslineAgain(t *testing.T) {
	def := claudeDef(t)
	start, project, _ := hostStart(t)
	path := filepath.Join(project, ConfigDirName, SettingsFileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"statusLine": {"type": "command", "command": "`+ctxloomStatusLineCommand()+`"}}`), 0o644))
	d, err := def.Settings.DeliverSettings(start, present.RootProjectRoot, engine.SettingsInputs{Statusline: true}, nil)
	require.NoError(t, err)
	require.Len(t, settingsClaimsIn(t, d, project), 1)
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

// Claude reaches the session's endpoint through its own relay, which claude
// spawns as a stdio server: only claude's descendant can post claude's wake
// as self-sent. The relay is handed the endpoint and its bearer in its env.
func TestDynamic_RendersTheSessionRelayAsAStdioEntry(t *testing.T) {
	def := claudeDef(t)
	entry := def.Dynamic.Endpoint(sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: "tok"})
	require.Equal(t, wire.MCPServer{
		Command: agent.CtxloomCommand(),
		Args:    []string{RelayCommand},
		Env:     map[string]string{EnvRelayURL: "http://127.0.0.1:1/mcp", EnvRelayBearer: "tok"},
	}, entry)
	require.Equal(t, "claude-relay", RelayCommand, "the command name is what an already-written MCP file spawns")
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
	t.Setenv("HOME", t.TempDir())
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

// TestDeliverMCP_SessionHomeRefusesAnUnrootedRun: with no session home
// advised there is no private file to claim into, and the project's is not
// a fallback.
func TestDeliverMCP_SessionHomeRefusesAnUnrootedRun(t *testing.T) {
	def := claudeDef(t)
	project := t.TempDir()
	start := present.New(present.OnHost(present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}}))
	_, err := def.MCP.DeliverMCP(start, present.RootSessionHome, engine.MCPInputs{Servers: map[string]wire.MCPServer{"probe": {Command: "probe-mcp"}}}, nil)
	require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
}
