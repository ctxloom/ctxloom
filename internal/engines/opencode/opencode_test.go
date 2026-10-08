package opencode

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

func def(t *testing.T) engine.Base {
	t.Helper()
	e, err := Build()
	require.NoError(t, err)
	return e.Root()
}

// start advises a project root and a session home on the host.
func start(t *testing.T) (present.Start, string, string) {
	t.Helper()
	project, home := t.TempDir(), t.TempDir()
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project, Engine: project},
		SessionHome: present.Root{Host: home, Engine: home},
	})), project, home
}

// The declaration: an opt-in engine, the TUI and the runner, whose every kind lands
// in the session home and nowhere else, carrying no hooks, resuming by key.
func TestBuild_Declaration(t *testing.T) {
	d := def(t)
	assert.Equal(t, EngineName, d.Name)
	assert.Equal(t, engine.DistributionOptIn, d.Distribution)
	assert.Equal(t, []engine.Mode{engine.Interactive, engine.Structured}, d.Modes)
	assert.Nil(t, d.Hooks)
	for kind, a := range d.Surfaces() {
		assert.Equal(t, []present.RootKind{present.RootSessionHome}, a.Traits().Roots, "%v", kind)
	}
	dc, ok := d.DelegatedChildren.Get()
	require.True(t, ok)
	assert.True(t, dc.ResumesByKey)
	assert.Empty(t, d.ProjectArtifacts, "nothing is delivered into a working tree")
}

// THE HOME (Q-B, Q-L FINAL): OPENCODE_CONFIG_DIR is the session home; each
// XDG base is a further var merged with opencode's app dir owned; the two
// test levers are session dirs; the database is native history inside the
// data base's owned dir.
func TestHome_RelocatesEveryPathOpencodeReads(t *testing.T) {
	h := Opencode{}.Home()
	require.NoError(t, h.Validate())
	assert.Equal(t, engine.HomeVar{Name: "OPENCODE_CONFIG_DIR", Subdir: "opencode"}, h.Vars[0])
	merged := map[string]string{}
	for _, v := range h.Vars[1:] {
		if v.Merge != nil {
			assert.Equal(t, []string{"opencode"}, v.Merge.Owns, v.Name)
			merged[v.Name] = v.Subdir
		}
	}
	assert.Equal(t, map[string]string{"XDG_DATA_HOME": ".xdg/data", "XDG_CONFIG_HOME": ".xdg/config", "XDG_STATE_HOME": ".xdg/state", "XDG_CACHE_HOME": ".xdg/cache"}, merged)
	assert.Contains(t, h.Vars, engine.HomeVar{Name: "OPENCODE_TEST_HOME", Subdir: "home"})
	assert.Contains(t, h.Vars, engine.HomeVar{Name: "OPENCODE_TEST_MANAGED_CONFIG_DIR", Subdir: "managed"})
	assert.Equal(t, ".xdg/data/opencode", h.TranscriptStoreRel)
}

// The context: the framed text as H/AGENTS.md, announced on
// OPENCODE_CONFIG_DIR; the project root is not a root opencode offers.
func TestDeliverContext(t *testing.T) {
	st, project, home := start(t)
	d, err := def(t).Context.DeliverContext(st, present.RootSessionHome, engine.ContextInputs{Text: []byte("rules")}, nil)
	require.NoError(t, err)
	path := filepath.Join(home, "AGENTS.md")
	assert.Equal(t, []string{path}, d.Files)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, agent.FrameProjectContext("rules"), string(body))
	assert.Equal(t, map[string]string{envConfigDir: home}, d.Presented.Env)
	_, err = def(t).Context.DeliverContext(st, present.RootProjectRoot, engine.ContextInputs{Text: []byte("rules")}, nil)
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(project, "AGENTS.md"))
}

// MCP: each server claimed under /mcp in H/opencode.json in opencode's
// dialect — the session endpoint remote with its bearer and OAuth
// detection off, a stdio server local — beside /$schema.
func TestDeliverMCP(t *testing.T) {
	st, _, home := start(t)
	ep := (&sessionEndpoint{}).Endpoint(sessions.Endpoint{URL: "http://127.0.0.1:4242/mcp", Credential: "tok"})
	d, err := def(t).MCP.DeliverMCP(st, present.RootSessionHome, engine.MCPInputs{Servers: map[string]wire.MCPServer{
		"ctxloom":  ep,
		"taskloom": {Command: "taskloom", Args: []string{"mcp"}, Env: map[string]string{"A": "1"}, SCM: "companion"},
	}}, nil)
	require.NoError(t, err)
	path := filepath.Join(home, "opencode.json")
	assert.Equal(t, map[string]string{envConfigDir: home}, d.Presented.Env)
	assert.Equal(t, []present.Claim{
		{Pointer: "/$schema", Value: configSchemaURL},
		{Pointer: "/mcp/ctxloom", Value: map[string]any{"type": "remote", "url": "http://127.0.0.1:4242/mcp", "enabled": true, "oauth": false, "headers": map[string]string{"Authorization": "Bearer tok"}}},
		{Pointer: "/mcp/taskloom", Value: map[string]any{"type": "local", "command": []string{"taskloom", "mcp"}, "enabled": true, "environment": map[string]string{"A": "1"}}, Via: "companion"},
	}, d.Claims[path])

	none, err := def(t).MCP.DeliverMCP(st, present.RootSessionHome, engine.MCPInputs{}, nil)
	require.NoError(t, err)
	assert.Empty(t, none.Claims, "no server, no file conjured")

	_, err = def(t).MCP.DeliverMCP(st, present.RootSessionHome, engine.MCPInputs{Servers: map[string]wire.MCPServer{"s": {ServedBy: wire.ServedBySessionEndpoint}}}, nil)
	require.ErrorIs(t, err, wire.ErrMCPServerUnrendered)
}

// Settings: each denied tool as /permission/<tool> = deny, once; the shell
// timeout's default on the env channel.
func TestDeliverSettings(t *testing.T) {
	st, _, home := start(t)
	d, err := def(t).Settings.DeliverSettings(st, present.RootSessionHome, engine.SettingsInputs{
		DenyTools: []string{"webfetch", "", "webfetch", "bash"}, Statusline: true,
		ShellTimeout: engine.ShellTimeout{Default: 2 * time.Minute, Max: 10 * time.Minute},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, []present.Claim{
		{Pointer: "/$schema", Value: configSchemaURL},
		{Pointer: "/permission/webfetch", Value: "deny"},
		{Pointer: "/permission/bash", Value: "deny"},
	}, d.Claims[filepath.Join(home, "opencode.json")])
	assert.Equal(t, "120000", d.Presented.Env[bashTimeoutEnv])
	assert.Equal(t, home, d.Presented.Env[envConfigDir])
}

// Commands: opencode's command files under H/commands, description and
// model in the frontmatter, {{var}} as $N; an empty body is refused.
func TestDeliverCommands(t *testing.T) {
	st, _, home := start(t)
	d, err := def(t).Commands.DeliverCommands(st, present.RootSessionHome, engine.CommandsInputs{Commands: []engine.CommandExport{
		{Name: "review/pr", Body: []byte("Review {{pr}} now"), Enabled: true, Model: "ctxloom/qwen"},
		{Name: "off", Body: []byte("x"), Enabled: false},
	}}, safefs.New())
	require.NoError(t, err)
	path := filepath.Join(home, "commands", "review-pr.md")
	assert.Equal(t, []string{path}, d.Files)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "---\ndescription: review/pr\nmodel: ctxloom/qwen\n---\n\nReview $1 now", string(body))
	assert.Equal(t, home, d.Presented.Env[envConfigDir])

	_, _, err = renderCommand(agent.CommandExport{Name: "empty", Content: "  \n"})
	require.Error(t, err)
}

// Skills: the Agent Skills standard with no reserved words, under
// H/skills/<name>/.
func TestDeliverSkills(t *testing.T) {
	st, _, home := start(t)
	skill := func(name string) engine.SkillExport {
		return engine.SkillExport{Name: name, Description: "d", Enabled: true, Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte("---\nname: " + name + "\ndescription: d\n---\n")}}}
	}
	d, err := def(t).Skills.DeliverSkills(st, present.RootSessionHome, engine.SkillsInputs{Skills: []engine.SkillExport{skill("claude-helper"), skill("Bad_Name")}}, safefs.New())
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(home, "skills", "claude-helper", "SKILL.md")}, d.Files, "opencode reserves no word; the standard still refuses a bad name")
}

// The permission model: build | plan | auto, an opencode permission object,
// resolution nearest-first with the flag over it, plan as the floor.
func TestPermissionModel(t *testing.T) {
	m := permissionModel{}
	require.NoError(t, m.Validate(map[string]any{keyMode: modePlan, keyPermission: map[string]any{"bash": map[string]any{"rm *": "deny", "*": "ask"}, "webfetch": "allow"}}))
	for name, doc := range map[string]map[string]any{
		"unknown key":      {"defaultMode": "x"},
		"unknown mode":     {keyMode: "bypass"},
		"bad action":       {keyPermission: map[string]any{"bash": "maybe"}},
		"bad pattern rule": {keyPermission: map[string]any{"bash": map[string]any{"*": 1}}},
		"not a map":        {keyPermission: "allow"},
	} {
		assert.Error(t, m.Validate(doc), name)
	}

	got, err := m.Resolve(engine.PostureRequest{})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{keyMode: modeBuild}, got)
	got, err = m.Resolve(engine.PostureRequest{Mode: modeAuto, Declared: []engine.Declaration{
		{Document: map[string]any{keyPermission: map[string]any{"bash": "deny"}}, From: "binding"},
		{Document: map[string]any{keyMode: modePlan, keyPermission: map[string]any{"edit": "deny"}}, From: "label"},
	}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{keyMode: modeAuto, keyPermission: map[string]any{"bash": "deny"}}, got)
	_, err = m.Resolve(engine.PostureRequest{Mode: "yolo"})
	require.Error(t, err)
	got, err = m.Resolve(engine.PostureRequest{Mode: "yolo", Degraded: true})
	require.NoError(t, err)
	assert.Equal(t, m.Floor(), got)

	assert.True(t, m.PlansFirst(map[string]any{keyMode: modePlan}))
	assert.Equal(t, []engine.PostureTransition{{Posture: modeBuild, Label: modeBuild, Default: true}}, m.Transitions(map[string]any{keyMode: modePlan}))
	assert.Nil(t, m.Transitions(map[string]any{keyMode: modeBuild}))
	assert.Equal(t, []engine.Sandbox{engine.SandboxFull}, m.Sandboxes("container-rootless"))
	assert.False(t, m.Reviewer())
}

// Auth: one required variable, the key by value in the run's credentials;
// unset is refused with the remedy; login is not a mode opencode has.
func TestAuth(t *testing.T) {
	a := opencodeAuth{}
	assert.Equal(t, []engine.AuthMode{engine.AuthToken}, a.Modes())
	c, err := a.Credentials(engine.AuthToken, func(k string) (string, bool) { return map[string]string{APIKeyEnv: "sk"}[k], k == APIKeyEnv })
	require.NoError(t, err)
	assert.Equal(t, map[string]string{APIKeyEnv: "sk"}, c.Env)
	_, err = a.Credentials(engine.AuthToken, func(string) (string, bool) { return "", false })
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Contains(t, err.Error(), APIKeyEnv)
	_, err = a.Credentials(engine.AuthLogin, nil)
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
}

// Exports: each item's opencode block decoded — opt-out honoured unless
// curated, the block's description over the authored one — and a block the
// schema refuses withholds the item, naming the engine.
func TestExports(t *testing.T) {
	e, err := Build()
	require.NoError(t, err)
	out, err := e.Exports(engine.Items{
		Commands: []engine.CommandItem{
			{Ref: "b/c1", Name: "c1", Description: "authored", Body: []byte("x"), Exports: []byte(`{"description":"block","model":"ctxloom/m"}`)},
			{Ref: "b/c2", Name: "c2", Exports: []byte(`{"enabled":false}`)},
			{Ref: "b/c3", Name: "c3", Exports: []byte(`{"enabled":false}`), Curated: true},
		},
		Skills: []engine.SkillItem{{Ref: "b/s", Name: "s", Description: "d", Exports: []byte(`{"enabled":false}`)}},
	})
	require.NoError(t, err)
	require.Len(t, out.Commands, 3)
	assert.Equal(t, engine.CommandExport{Name: "c1", Body: []byte("x"), Enabled: true, Description: "block", Model: "ctxloom/m"}, out.Commands[0])
	assert.False(t, out.Commands[1].Enabled)
	assert.True(t, out.Commands[2].Enabled, "a curated command exports regardless")
	assert.False(t, out.Skills[0].Enabled)
	assert.Empty(t, out.HookEvent, "opencode fires no hook event")

	_, err = e.Exports(engine.Items{Commands: []engine.CommandItem{{Ref: "b/bad", Exports: []byte(`{"argument_hint":"x"}`)}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opencode: export block")
}

// The engine facts it declares absent, and the hook codec that refuses.
func TestFacts(t *testing.T) {
	e, err := Build()
	require.NoError(t, err)
	_, err = e.Container()
	var unsupported engine.ErrUnsupported
	require.True(t, errors.As(err, &unsupported))
	_, approvals := e.Approvals().Get()
	assert.True(t, e.Approvals().Decided())
	assert.False(t, approvals, "approvals are declared absent")
	_, wake := e.Wake().Get()
	assert.False(t, wake)
	_, trust := e.Trust().Get()
	assert.False(t, trust)
	assert.Empty(t, e.Transcripts())
	_, err = e.TranscriptSession("x.jsonl")
	require.True(t, errors.As(err, &unsupported))
	_, err = e.Hooks().Decode("pre_tool", []byte(`{}`))
	require.True(t, errors.As(err, &unsupported))
}

// The backend: the projection onto this kind, configured from the typed
// config; another engine's config is refused.
func TestBackend(t *testing.T) {
	e, err := Build()
	require.NoError(t, err)
	h := e.(agent.Hosted)
	b := h.Backend(nil)
	cfg := h.NewConfig().(*Config)
	cfg.BinaryPath = "/opt/oc"
	require.NoError(t, b.(agent.Configurable).Configure(cfg))
	assert.Equal(t, "/opt/oc", b.(*kit.ProjectedBackend).GetBinaryPath())
	require.ErrorIs(t, b.(agent.Configurable).Configure(otherConfig{}), agent.ErrBackendConfigType)
	assert.Equal(t, string(EngineName), cfg.BackendType())
	_, ok := h.HookGlobalScope()
	assert.False(t, ok)
	st, err := h.SettingsReader(agent.SettingsOptions{}).Status(t.TempDir())
	require.NoError(t, err)
	assert.False(t, st.Wired())
}

type otherConfig struct{}

func (otherConfig) BackendType() string { return "other" }

// The context file is written through the fs handed in (the static
// writer's).
func TestDeliverContext_ThroughTheGivenFs(t *testing.T) {
	st, _, home := start(t)
	mem := afero.NewMemMapFs()
	_, err := def(t).Context.DeliverContext(st, present.RootSessionHome, engine.ContextInputs{Text: []byte("x")}, mem)
	require.NoError(t, err)
	b, err := afero.ReadFile(mem, filepath.Join(home, "AGENTS.md"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(b), "x"))
}
