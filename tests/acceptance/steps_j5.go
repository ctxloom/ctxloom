//go:build acceptance

// J5: "one shared profile, reaching four engines in their own native format"
// (j5_multi_engine.feature). Outline A materializes Carol's team profile for
// each of the four engines and PARSES the generated file in its own format
// (JSON, TOML, or markdown) to assert the fragment/server/hook/skill payload
// actually landed — never a bare file-exists or a substring-of-a-key-name.
// Outline B (@live) plants a sentinel in a fragment and drives a real claude
// or antigravity engine, asserting the sentinel returns through the actual
// reply (steps_live.go's liveAgents wires exactly these two).
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/pelletier/go-toml/v2"
)

// Distinctive marker strings the shared "team" bundle carries, so a
// materialized surface can be checked for exactly this payload (ASSERTION
// DISCIPLINE — never a bare file-exists or exit-code proxy).
const (
	j5ContextMarker      = "J5-CONTEXT-MARKER-3f8a91"
	j5SkillMarker        = "J5-SKILL-BODY-MARKER-7c2d64"
	j5MCPCommand         = "j5-mcp-tool-9e1b52"
	j5HookCommand        = "echo J5-HOOK-COMMAND-4a6f18"
	j5LiveSentinel       = "J5-LIVE-SENTINEL-PHRASE-2b9dfa"
	j5HandAuthoredMarker = "J5-HAND-AUTHORED-MARKER-6d2e73: always use tabs, never spaces"
)

// j5State is J5's fixture state: which engine row (Outline A) is currently
// materialized, and into which target dir.
type j5State struct {
	engine string // current Examples row's backend name (claude-code/codex/kiro/antigravity)
	target string // profile materialize --target dir for this row (relative to project root)
}

func j5Of(w *World) *j5State {
	if w.j5 == nil {
		w.j5 = &j5State{}
	}
	return w.j5
}

// j5TeamBundleYAML renders the shared "team" bundle: one fragment, one skill,
// one MCP server, one hook — all first-party (authored directly in the
// project, not pulled from a remote), so the executable trust gate exempts
// them (operations/trust.go:265's LOCAL rule) and no signing ceremony is
// needed to prove materialization.
func j5TeamBundleYAML() string {
	return fmt.Sprintf(`version: "1.0.0"
description: J5 shared team bundle
fragments:
  onboarding-context:
    content: %q
skills:
  onboarding:
    description: J5 onboarding skill
    content: %q
mcp:
  toolserver:
    command: %q
    args: ["--flag"]
hooks:
  session_start:
    - command: %q
      type: command
`, j5ContextMarker, j5SkillMarker, j5MCPCommand, j5HookCommand)
}

func registerJ5Steps(ctx *godog.ScenarioContext) {
	// --- Outline A: materialization (hermetic) --------------------------------

	ctx.Step(`^Carol's team profile carries a shared fragment, skill, MCP server, and hook$`, func(c context.Context) error {
		w := worldFrom(c)
		j5Of(w)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", "version: 4\n"); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/content/bundles/team.yaml", j5TeamBundleYAML()); err != nil {
			return err
		}
		return runOK(w, "profile", "create", "team", "-b", "team", "-d", "J5 shared team profile")
	})

	ctx.Step(`^Alice materializes the team profile for (\S+)$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		j5 := j5Of(w)
		j5.engine = engine
		j5.target = "out-" + engine
		return runOK(w, "profile", "materialize", "team", "--target", j5.target, "--backend", engine)
	})

	ctx.Step(`^the materialized (\S+) context carries the shared fragment's marker, in its own native shape$`, func(c context.Context, engine string) error {
		return j5AssertContext(worldFrom(c), engine)
	})

	ctx.Step(`^the materialized (\S+) MCP configuration carries the shared server's command, in its own native shape$`, func(c context.Context, engine string) error {
		return j5AssertMCP(worldFrom(c), engine)
	})

	ctx.Step(`^the materialized (\S+) hook configuration carries the shared hook's command, in its own native shape$`, func(c context.Context, engine string) error {
		return j5AssertHook(worldFrom(c), engine)
	})

	ctx.Step(`^the materialized (\S+) skill file carries the shared skill's body, in its own native shape$`, func(c context.Context, engine string) error {
		return j5AssertSkill(worldFrom(c), engine)
	})

	// --- Regression: lanky-plop (P0 data loss) --------------------------------
	//
	// A hand-authored context file must survive materialization byte-for-byte
	// outside ctxloom's managed markers. The hand-authored step writes directly
	// into the SAME target dir "Alice materializes the team profile for
	// <engine>" derives deterministically ("out-"+engine), so it must run
	// BEFORE that step in the Gherkin — Given/And ordering, not execution order
	// inferred from step registration.

	ctx.Step(`^Alice's team already hand-authored (\S+) for (\S+) with their own conventions$`, func(c context.Context, file, engine string) error {
		w := worldFrom(c)
		j5 := j5Of(w)
		j5.engine = engine
		j5.target = "out-" + engine
		rel := filepath.Join(j5.target, file)
		return w.env.WriteFile(rel, "# Team conventions\n"+j5HandAuthoredMarker+"\n")
	})

	ctx.Step(`^(\S+) still carries Alice's hand-authored conventions, byte-for-byte$`, func(c context.Context, file string) error {
		w := worldFrom(c)
		j5 := j5Of(w)
		rel := filepath.Join(j5.target, file)
		return j5FileContains(w, rel, j5HandAuthoredMarker)
	})

	// --- Outline B: live (@live, claude + antigravity only) -------------------
	//
	// "a real (\S+) agent is available" is steps_live.go's shared gate step,
	// reused VERBATIM (godog rejects an ambiguous second match for the same
	// step text — steps_j3.go:229-235) — it self-skips the whole scenario
	// (godog.ErrSkip) without credentials, exactly as distill_live.feature's
	// own Scenario Outlines already rely on.

	ctx.Step(`^Carol's team profile carries a fragment with a sentinel marker$`, func(c context.Context) error {
		w := worldFrom(c)
		body := fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  marker:\n    content: %q\n", j5LiveSentinel)
		if err := w.env.WriteFile(".ctxloom/content/bundles/team.yaml", body); err != nil {
			return err
		}
		return runOK(w, "profile", "create", "team", "-b", "team", "-d", "J5 shared team profile")
	})

	ctx.Step(`^Alice asks her (\S+) assistant to repeat the sentinel it can see$`, func(c context.Context, _ string) error {
		w := worldFrom(c)
		_ = w.env.Run("run", "--print", "--profile", "team",
			"Please repeat, verbatim and in full, the one distinct marker phrase you can see in your context. Output nothing else.")
		return nil
	})

	ctx.Step(`^its reply contains the sentinel marker$`, func(c context.Context) error {
		w := worldFrom(c)
		if !strings.Contains(w.env.LastOutput(), j5LiveSentinel) {
			return fmt.Errorf("assistant reply does not contain the sentinel marker; output:\n%s", w.env.LastOutput())
		}
		return nil
	})
}

// j5FileContains reads a project-relative file and asserts it contains want.
func j5FileContains(w *World, rel, want string) error {
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read %q: %w", rel, err)
	}
	// Surface a short, marker-centered excerpt of the delivered file to the
	// @doc capture sidecar (set-and-consume; no-op when capture is off) — the
	// real bytes that landed, not a restatement of the assertion. On the host
	// machines these files run against, materialize also folds in whatever
	// companions are actually on PATH (ltk, taskloom), so the full file can be
	// much longer than the one marker line this scenario cares about; the
	// excerpt keeps the published page short and on-topic.
	w.docStepMaterialized = fmt.Sprintf("%s:\n%s", rel, j5Excerpt(body, want, 1))
	if !strings.Contains(body, want) {
		return fmt.Errorf("file %q does not contain %q; content:\n%s", rel, want, body)
	}
	return nil
}

// j5Excerpt returns a short window of body centered on the first line
// containing marker: up to context lines before and after. Falls back to the
// full (trimmed) body if marker never appears on its own line, so a caller
// never loses evidence to a formatting surprise.
func j5Excerpt(body, marker string, context int) string {
	lines := strings.Split(body, "\n")
	idx := -1
	for i, line := range lines {
		if strings.Contains(line, marker) {
			idx = i
			break
		}
	}
	if idx == -1 {
		return strings.TrimSpace(body)
	}
	start := idx - context
	if start < 0 {
		start = 0
	}
	end := idx + context + 1
	if end > len(lines) {
		end = len(lines)
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

// engineContextRelPath returns dir-relative path to an engine's own native
// context surface (internal/{claude,codex,kiro,antigravity}/surfaces.go).
// Shared engine-axis knowledge: J5's own materialization outline uses it
// below, and J4's onboarding journey reuses it rather than re-deriving a
// second copy of the same per-engine path table (steps_j4_onboarding.go's
// "Bob starts a session on <engine>" outline). codex is now a case here:
// `profile materialize` used to leave its context surface a silent no-op
// (taskloom tiny-ooze — keyed on agent.SurfaceInputs.Fragments, which
// materialize never populates); codex's context surface now ALSO writes
// AGENTS.md from agent.SurfaceInputs.Context, which materialize does
// populate (internal/codex/surfaces.go's agentsMDSurface).
func engineContextRelPath(dir, engine string) (string, error) {
	switch engine {
	case "claude-code":
		return filepath.Join(dir, "CLAUDE.md"), nil
	case "codex":
		return filepath.Join(dir, "AGENTS.md"), nil
	case "kiro":
		return filepath.Join(dir, ".kiro", "steering", "ctxloom-context.md"), nil
	case "antigravity":
		return filepath.Join(dir, ".agents", "AGENTS.md"), nil
	default:
		return "", fmt.Errorf("unknown engine %q for native context surface", engine)
	}
}

// j5AssertContext dispatches to the right native context surface per engine.
func j5AssertContext(w *World, engine string) error {
	j5 := j5Of(w)
	rel, err := engineContextRelPath(j5.target, engine)
	if err != nil {
		return err
	}
	return j5FileContains(w, rel, j5ContextMarker)
}

// j5ReadJSON reads a project-relative file and parses it as a generic JSON
// document.
func j5ReadJSON(w *World, rel string) (map[string]any, error) {
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", rel, err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return nil, fmt.Errorf("parse %q as JSON: %w (content:\n%s)", rel, err, body)
	}
	return doc, nil
}

// j5ReadTOML reads a project-relative file and parses it as a generic TOML
// document (codex's .codex/config.toml).
func j5ReadTOML(w *World, rel string) (map[string]any, error) {
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", rel, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal([]byte(body), &doc); err != nil {
		return nil, fmt.Errorf("parse %q as TOML: %w (content:\n%s)", rel, err, body)
	}
	return doc, nil
}

// j5AssertMCP parses each engine's MCP registry in its own native format
// (JSON's shared "mcpServers" table for claude/kiro/antigravity — the same
// agent.MCPFileConfig reconciler backs all three — or codex's TOML
// "mcp_servers" table folded into config.toml) and asserts the shared
// server's command landed under its name.
func j5AssertMCP(w *World, engine string) error {
	j5 := j5Of(w)
	dir := j5.target
	var (
		doc map[string]any
		err error
		key string
		rel string
	)
	switch engine {
	case "claude-code":
		rel = filepath.Join(dir, ".mcp.json")
		doc, err = j5ReadJSON(w, rel)
		key = "mcpServers"
	case "kiro":
		rel = filepath.Join(dir, ".kiro", "settings", "mcp.json")
		doc, err = j5ReadJSON(w, rel)
		key = "mcpServers"
	case "antigravity":
		rel = filepath.Join(dir, ".agents", "mcp_config.json")
		doc, err = j5ReadJSON(w, rel)
		key = "mcpServers"
	case "codex":
		// codex has NO distinct MCP file — it folds "mcp_servers" into the
		// SAME config.toml the hooks assertion below also reads.
		rel = filepath.Join(dir, ".codex", "config.toml")
		doc, err = j5ReadTOML(w, rel)
		key = "mcp_servers"
	default:
		return fmt.Errorf("j5: unknown engine %q", engine)
	}
	if err != nil {
		return err
	}
	top, ok := doc[key].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: no %q table in the generated MCP configuration; parsed: %+v", engine, key, doc)
	}
	srv, ok := top["toolserver"].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: no %q server entry under %q; parsed: %+v", engine, "toolserver", key, top)
	}
	cmd, _ := srv["command"].(string)
	// Surface the real parsed server entry — the actual command (and args, if
	// any) this engine's OWN file carries under its own table name (mcpServers
	// vs codex's mcp_servers) — to the @doc capture sidecar (set-and-consume;
	// no-op when capture is off), rather than letting the published page show
	// only a bare claim.
	evidence := fmt.Sprintf("%s → %s.toolserver\n  command: %s", rel, key, cmd)
	if args := j5FormatArgs(srv["args"]); args != "" {
		evidence += fmt.Sprintf("\n  args:    %s", args)
	}
	w.docStepMaterialized = evidence
	if cmd != j5MCPCommand {
		return fmt.Errorf("%s's MCP server %q has command %q, want %q", engine, "toolserver", cmd, j5MCPCommand)
	}
	return nil
}

// j5FormatArgs renders a decoded JSON/TOML args array (a []any of strings) as
// a short bracketed, space-joined native-ish snippet for evidence display, or
// "" if there are none.
func j5FormatArgs(v any) string {
	list, _ := v.([]any)
	if len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// j5HookCommandsFrom walks a decoded hooks event value — which is, depending
// on the engine, either a flat list of {matcher, command} entries (kiro) or a
// list of {matcher, hooks: [{type, command}]} groups (claude, antigravity,
// codex) — and collects every command found, so one walker serves every
// engine's own shape.
func j5HookCommandsFrom(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if cmd, ok := m["command"].(string); ok && cmd != "" {
			out = append(out, cmd)
		}
		if nested, ok := m["hooks"]; ok {
			out = append(out, j5HookCommandsFrom(nested)...)
		}
	}
	return out
}

// j5AssertHook parses each engine's own hook configuration (claude's
// .claude/settings.json "hooks.SessionStart", kiro's agent JSON
// "hooks.agentSpawn" — kiro diverts session_start to its own event name — antigravity's
// separate .agents/hooks.json "hooks.SessionStart", codex's config.toml
// "hooks.SessionStart" folded into the same file MCP lives in) and asserts
// the shared hook's command landed under the right event.
func j5AssertHook(w *World, engine string) error {
	j5 := j5Of(w)
	dir := j5.target
	var (
		doc   map[string]any
		event string
		err   error
		rel   string
	)
	switch engine {
	case "claude-code":
		rel = filepath.Join(dir, ".claude", "settings.json")
		doc, err = j5ReadJSON(w, rel)
		event = "SessionStart"
	case "kiro":
		rel = filepath.Join(dir, ".kiro", "agents", "ctxloom.json")
		doc, err = j5ReadJSON(w, rel)
		event = "agentSpawn"
	case "antigravity":
		rel = filepath.Join(dir, ".agents", "hooks.json")
		doc, err = j5ReadJSON(w, rel)
		event = "SessionStart"
	case "codex":
		rel = filepath.Join(dir, ".codex", "config.toml")
		doc, err = j5ReadTOML(w, rel)
		event = "SessionStart"
	default:
		return fmt.Errorf("j5: unknown engine %q", engine)
	}
	if err != nil {
		return err
	}
	top, ok := doc["hooks"].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: no %q table in the generated hook configuration; parsed: %+v", engine, "hooks", doc)
	}
	cmds := j5HookCommandsFrom(top[event])
	found := false
	for _, cmd := range cmds {
		if cmd == j5HookCommand {
			found = true
			break
		}
	}
	// Surface the real event name and command this engine's own hook
	// configuration carries to the @doc capture sidecar (set-and-consume;
	// no-op when capture is off) — kiro diverts session_start to its own
	// "agentSpawn" event name, which the note below makes visible rather than
	// leaving it as a fact only findable by re-reading the Go source.
	note := ""
	if engine == "kiro" {
		note = "  (kiro diverts session_start → agentSpawn, its own event name)"
	}
	w.docStepMaterialized = fmt.Sprintf("%s → hooks.%s%s\n  command: %s", rel, event, note, j5HookCommand)
	if found {
		return nil
	}
	return fmt.Errorf("%s's %q hooks %v do not include the shared hook's command %q", engine, event, cmds, j5HookCommand)
}

// j5AssertSkill asserts the shared skill's body reached each engine's own
// skill file path — claude/codex flatten the "<bundle>/<item>" export name's
// slash to a dash (backends/commandfiles.go's exportNames), while kiro and
// antigravity preserve it as a subdirectory (internal/{kiro,antigravity}
// capabilities.go).
func j5AssertSkill(w *World, engine string) error {
	j5 := j5Of(w)
	dir := j5.target
	var rel string
	switch engine {
	case "claude-code":
		rel = filepath.Join(dir, ".claude", "commands", "team-onboarding.md")
	case "codex":
		rel = filepath.Join(dir, ".codex", "prompts", "team-onboarding.md")
	case "kiro":
		rel = filepath.Join(dir, ".kiro", "skills", "team", "onboarding", "SKILL.md")
	case "antigravity":
		rel = filepath.Join(dir, ".agents", "skills", "team", "onboarding.md")
	default:
		return fmt.Errorf("j5: unknown engine %q", engine)
	}
	return j5FileContains(w, rel, j5SkillMarker)
}
