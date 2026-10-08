package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// qwenBody is a label body as config hands it to the runner — through the
// coordinator's wire, whose numbers arrive as float64.
func qwenBody() map[string]any {
	return map[string]any{
		"type": "opencode", "model": "qwen3.8-27b", "context_window": float64(131072),
		"provider":      map[string]any{"base_url": "http://192.168.1.20:8081/v1", "output_limit": float64(8192), "header_timeout_ms": float64(0), "chunk_timeout_ms": float64(300000)},
		"model_options": map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}, "temperature": 0.7},
		"permissions":   map[string]any{"approver": "none", "mode": "build"},
	}
}

func session(t *testing.T, body map[string]any) engine.Session {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	s := engine.Session{
		Label:   engine.LabelConfig{Label: "qwen", Model: "qwen3.8-27b", Body: body},
		Mode:    engine.Structured,
		Roots:   present.Paths{ProjectRoot: present.Root{Host: filepath.Join(dir, "project"), Engine: filepath.Join(dir, "project")}, SessionHome: present.Root{Host: home, Engine: home}},
		WorkDir: filepath.Join(dir, "project"),
		Prompt:  "do the thing",
	}
	s.Home = engine.BindHome(Opencode{}.Home().Vars, s.Roots.SessionHome)
	require.NoError(t, os.MkdirAll(s.WorkDir, 0o700))
	return s
}

func configDoc(t *testing.T, ex engine.Exec) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(ex.Env[envConfigContent]), &doc))
	return doc
}

// THE LAUNCH: argv parses against the declared grammar and is exactly the
// structured protocol; the prompt rides stdin; the binary is opencode.
func TestExec_ArgvIsTheStructuredProtocol(t *testing.T) {
	inst := bind(t, session(t, qwenBody()))
	ex, err := inst.Exec(nil)
	require.NoError(t, err)
	assert.Equal(t, "opencode", ex.Binary)
	assert.Equal(t, []string{"run", "--format", "json", "--thinking", "--model", "ctxloom/qwen3.8-27b"}, ex.Args)
	assert.Equal(t, []byte("do the thing"), ex.StdinPrompt)
	assert.False(t, ex.Interactive)
	_, err = structuredGrammar.ParseArgv(ex.Args)
	require.NoError(t, err)

	require.NoError(t, inst.Resume("ses_abc"))
	ex, err = inst.Exec(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "--format", "json", "--thinking", "--model", "ctxloom/qwen3.8-27b", "--session", "ses_abc"}, ex.Args, "a resumed instance continues its session")
	_, err = structuredGrammar.ParseArgv(ex.Args)
	require.NoError(t, err)
}

// The binary is the session's, else the label's binary_path, else
// opencode; the label's args lead.
func TestExec_BinaryAndLabelArgs(t *testing.T) {
	body := qwenBody()
	body["binary_path"] = "/opt/opencode/bin/opencode"
	body["args"] = []any{"--print-logs"}
	ex, err := bind(t, session(t, body)).Exec(nil)
	require.NoError(t, err)
	assert.Equal(t, "/opt/opencode/bin/opencode", ex.Binary)
	assert.Equal(t, "--print-logs", ex.Args[0])

	s := session(t, body)
	s.Label.Binary = "/runner/opencode"
	ex, err = bind(t, s).Exec(nil)
	require.NoError(t, err)
	assert.Equal(t, "/runner/opencode", ex.Binary)
}

// The env: every home var at its bound path, the isolation switches, the
// composed config and the posture — and never the key's value.
func TestExec_Env(t *testing.T) {
	s := session(t, qwenBody())
	ex, err := bind(t, s).Exec([]present.Presentation{{Env: map[string]string{envConfigDir: s.Roots.SessionHome.Engine}}})
	require.NoError(t, err)
	for _, h := range s.Home {
		assert.Equal(t, h.Path, ex.Env[h.Var], h.Var)
	}
	assert.Equal(t, s.Roots.SessionHome.Engine, ex.Env[envConfigDir], "OPENCODE_CONFIG_DIR names the session home")
	assert.Equal(t, filepath.Join(s.Roots.SessionHome.Engine, ".xdg", "data"), ex.Env["XDG_DATA_HOME"])
	assert.Equal(t, filepath.Join(s.Roots.SessionHome.Engine, "home"), ex.Env[envTestHome])
	assert.Equal(t, filepath.Join(s.Roots.SessionHome.Engine, "managed"), ex.Env[envManagedConfig])
	for _, v := range []string{envDisableClaudeCode, envDisableAutoupdate, envDisableModelsFetch, envDisableExternalSkills, envDisableProjectConfig} {
		assert.Equal(t, "1", ex.Env[v], v)
	}
	assert.Equal(t, "{}", ex.Env[envPermission])
	assert.NotContains(t, ex.Env, APIKeyEnv, "the key rides the run's credentials, never Exec")
	assert.Contains(t, ex.Env[envConfigContent], "{env:"+APIKeyEnv+"}", "the config names the key by reference")
}

// OPENCODE_CONFIG_CONTENT: the label's endpoint as the only enabled
// provider, its window as limit.context (never assumed), its output limit,
// its model options verbatim, a header timeout of 0 as false (Q-E), and the
// policy defaults for a structured run (Q-F/Q-J): no snapshot, no title,
// no sharing, no autoupdate.
func TestExec_ConfigContent(t *testing.T) {
	ex, err := bind(t, session(t, qwenBody())).Exec(nil)
	require.NoError(t, err)
	doc := configDoc(t, ex)
	assert.Equal(t, "ctxloom/qwen3.8-27b", doc["model"])
	assert.Equal(t, "ctxloom/qwen3.8-27b", doc["small_model"])
	assert.Equal(t, []any{"ctxloom"}, doc["enabled_providers"])
	assert.Equal(t, false, doc["autoupdate"])
	assert.Equal(t, false, doc["snapshot"])
	assert.Equal(t, "disabled", doc["share"])
	assert.Equal(t, map[string]any{"title": map[string]any{"disable": true}}, doc["agent"])
	assert.Equal(t, configSchemaURL, doc["$schema"])
	p := doc["provider"].(map[string]any)["ctxloom"].(map[string]any)
	assert.Equal(t, "@ai-sdk/openai-compatible", p["npm"])
	assert.Equal(t, map[string]any{"baseURL": "http://192.168.1.20:8081/v1", "apiKey": "{env:CTXLOOM_OPENCODE_API_KEY}", "headerTimeout": false, "chunkTimeout": float64(300000)}, p["options"])
	m := p["models"].(map[string]any)["qwen3.8-27b"].(map[string]any)
	assert.Equal(t, map[string]any{"context": float64(131072), "output": float64(8192)}, m["limit"])
	assert.Equal(t, map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}, "temperature": 0.7}, m["options"])
	assert.NotContains(t, doc, "instructions", "no project AGENTS.md to re-add")
}

// The label's own switches win; a positive header timeout passes as is;
// unset timeouts are opencode's defaults (absent).
func TestExec_ConfigContentHonoursTheLabel(t *testing.T) {
	body := qwenBody()
	body["snapshot"], body["title_generation"], body["share"] = true, true, "manual"
	body["provider"] = map[string]any{"base_url": "http://h:1/v1", "output_limit": 10, "header_timeout_ms": 900000}
	doc := configDoc(t, mustExec(t, bind(t, session(t, body))))
	assert.Equal(t, true, doc["snapshot"])
	assert.Equal(t, "manual", doc["share"])
	assert.Equal(t, map[string]any{"title": map[string]any{"disable": false}}, doc["agent"])
	opts := doc["provider"].(map[string]any)["ctxloom"].(map[string]any)["options"].(map[string]any)
	assert.Equal(t, float64(900000), opts["headerTimeout"])
	assert.NotContains(t, opts, "chunkTimeout")
}

func mustExec(t *testing.T, inst *instance) engine.Exec {
	t.Helper()
	ex, err := inst.Exec(nil)
	require.NoError(t, err)
	return ex
}

// An untrusted repository (every one: opencode has no trust record) has its
// project config disabled and its AGENTS.md re-added by engine path; a
// trusted one keeps its own config and needs neither.
func TestExec_TheProjectsAgentsFileUnderTrust(t *testing.T) {
	s := session(t, qwenBody())
	require.NoError(t, os.MkdirAll(s.Roots.ProjectRoot.Host, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(s.Roots.ProjectRoot.Host, "AGENTS.md"), []byte("# rules"), 0o600))
	ex := mustExec(t, bind(t, s))
	assert.Equal(t, []any{filepath.Join(s.Roots.ProjectRoot.Engine, "AGENTS.md")}, configDoc(t, ex)["instructions"])
	assert.Equal(t, "1", ex.Env[envDisableProjectConfig])

	s.Trust = engine.TrustTrusted
	ex = mustExec(t, bind(t, s))
	assert.NotContains(t, configDoc(t, ex), "instructions")
	assert.NotContains(t, ex.Env, envDisableProjectConfig)
}

// A label with no endpoint (the conformance session) still composes: the
// policy without a provider.
func TestExec_WithoutAnEndpoint(t *testing.T) {
	s := session(t, nil)
	s.Label.Model = ""
	ex := mustExec(t, bind(t, s))
	doc := configDoc(t, ex)
	assert.NotContains(t, doc, "provider")
	assert.NotContains(t, doc, "model")
	assert.Equal(t, []string{"run", "--format", "json", "--thinking"}, ex.Args)
}

// A body the typed config cannot take is refused at Instance, naming the
// engine.
func TestInstance_RefusesAnUndecodableBody(t *testing.T) {
	e, err := Build()
	require.NoError(t, err)
	_, err = e.Instance(engine.Session{Label: engine.LabelConfig{Body: map[string]any{"provider": "not a map"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opencode")
}

// THE POSTURE PER TURN: --auto exactly when the turn runs at auto, and the
// turn's OPENCODE_PERMISSION over the session's — plan's denies included.
func TestTurn_PostureRidesArgvAndEnv(t *testing.T) {
	s := session(t, qwenBody())
	s.Permission = engine.PermissionPolicy{Posture: engine.Posture{Engine: EngineName, Document: map[string]any{
		keyMode: modeAuto, keyPermission: map[string]any{"webfetch": "deny"},
	}}}
	inst := bind(t, s)
	ex := mustExec(t, inst)
	assert.Contains(t, ex.Args, flagAuto, "the session runs at auto")

	args, err := inst.turnArgv(ex, engine.Turn{Resume: "ses_1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "--format", "json", "--thinking", "--model", "ctxloom/qwen3.8-27b", "--session", "ses_1", "--auto"}, args)
	env, err := inst.turnEnv(ex, engine.Turn{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"webfetch":"deny"}`, env[envPermission])

	plan := engine.Turn{Posture: engine.TurnPosture{Mode: modePlan}}
	args, err = inst.turnArgv(ex, plan)
	require.NoError(t, err)
	assert.NotContains(t, args, flagAuto, "a plan turn does not approve what is left to ask")
	env, err = inst.turnEnv(ex, plan)
	require.NoError(t, err)
	assert.JSONEq(t, `{"webfetch":"deny","edit":{"*":"deny"},"bash":{"*":"deny"}}`, env[envPermission])

	_, err = inst.turnEnv(ex, engine.Turn{Posture: engine.TurnPosture{Mode: "yolo"}})
	require.Error(t, err, "a turn mode outside the vocabulary is refused")
}

// A resumed Exec's key is not named twice.
func TestTurn_ArgvNamesTheSessionOnce(t *testing.T) {
	inst := bind(t, session(t, qwenBody()))
	require.NoError(t, inst.Resume("ses_a"))
	ex := mustExec(t, inst)
	args, err := inst.turnArgv(ex, engine.Turn{Resume: "ses_b"})
	require.NoError(t, err)
	n := 0
	for _, a := range args {
		if a == flagSession {
			n++
		}
	}
	assert.Equal(t, 1, n)
}
