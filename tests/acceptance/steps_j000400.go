//go:build acceptance

// J000400: "one shared profile, reaching three engines in their own native format"
// (j000400_multi_engine.feature). Outline A materializes Carol's team profile for
// each of the three engines and PARSES the generated file in its own format
// (JSON, TOML, or markdown) to assert the fragment/server/hook/command payload
// actually landed — never a bare file-exists or a substring-of-a-key-name.
// Outline B (@live) plants a sentinel in a fragment and drives a real engine,
// asserting the sentinel returns through the actual reply (steps_live.go's
// liveAgents wires each row).
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/pelletier/go-toml/v2"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Distinctive marker strings the shared "team" bundle carries, so a
// materialized surface can be checked for exactly this payload (ASSERTION
// DISCIPLINE — never a bare file-exists or exit-code proxy).
const (
	j000400ContextMarker      = "J000400-CONTEXT-MARKER-3f8a91"
	j000400CommandMarker      = "J000400-COMMAND-BODY-MARKER-7c2d64"
	j000400MCPCommand         = "j000400-mcp-tool-9e1b52"
	j000400HookCommand        = "echo J000400-HOOK-COMMAND-4a6f18"
	j000400LiveSentinel       = "J000400-LIVE-SENTINEL-PHRASE-2b9dfa"
	j000400HandAuthoredMarker = "J000400-HAND-AUTHORED-MARKER-6d2e73: always use tabs, never spaces"
)

// j000400State is J000400's fixture state: which engine row (Outline A) is currently
// materialized, and into which target dir.
type j000400State struct {
	engine string // current Examples row's backend name (claude-code/codex/kiro)
	target string // profile materialize --target dir for this row (relative to project root)

	// handAuthoredBytes is the EXACT content the hand-authored fixture wrote
	// before any materialize ran. The byte-for-byte regression compares the
	// post-materialize file's non-managed regions against this snapshot, so
	// "byte-for-byte" in the scenario title is measured rather than sampled.
	handAuthoredBytes string
}

// j000400 managed-section markers, written out as literals rather than imported
// from internal/shared/agent. They are a USER-VISIBLE contract — these exact
// bytes land in a team's own CLAUDE.md/AGENTS.md — so the regression that
// guards a team's hand-authored content must not be able to move in lockstep
// with the production code it is guarding. If ctxloom ever changes its marker
// text, this test going red is the correct outcome, not a maintenance chore.
const (
	j000400ManagedBegin = "<!-- ctxloom:context:begin (managed — do not edit between markers) -->"
	j000400ManagedEnd   = "<!-- ctxloom:context:end -->"
)

// j000400StripManagedSection removes the ctxloom-managed section from body,
// returning everything outside it — the region ctxloom promises to preserve
// byte-for-byte. Implemented HERE rather than reusing
// agent.StripManagedSection for the same independence reason as the markers
// above: an assertion that shares its parsing with the code under test cannot
// observe that code losing content.
//
// An unterminated begin marker drops through to EOF (the section is
// ctxloom's to own), matching what the production writer does.
func j000400StripManagedSection(body string) string {
	begin := strings.Index(body, j000400ManagedBegin)
	if begin < 0 {
		return body
	}
	rest := body[begin+len(j000400ManagedBegin):]
	end := strings.Index(rest, j000400ManagedEnd)
	if end < 0 {
		return body[:begin]
	}
	return body[:begin] + strings.TrimLeft(rest[end+len(j000400ManagedEnd):], "\n")
}

func j000400Of(w *World) *j000400State {
	if w.j000400 == nil {
		w.j000400 = &j000400State{}
	}
	return w.j000400
}

// j000400TargetFor is the one definition of J000400's per-engine materialize target
// dir (this used to be "out-"+engine, independently written at two
// step handlers -- one connascence-of-meaning bug waiting for the two copies
// to drift).
func j000400TargetFor(engine string) string {
	return "out-" + engine
}

// j000400TeamBundleYAML renders the shared "team" bundle: one fragment, one command,
// one MCP server, one hook — all first-party (authored directly in the
// project, not pulled from a remote), so the executable trust gate exempts
// them (operations/trust.go:265's LOCAL rule) and no signing ceremony is
// needed to prove materialization.
func j000400TeamBundleYAML() string {
	return fmt.Sprintf(`version: "1.0.0"
description: J000400 shared team bundle
fragments:
  onboarding-context:
    content: %q
commands:
  onboarding:
    description: J000400 onboarding command
    content: %q
mcp:
  toolserver:
    command: %q
    args: ["--flag"]
hooks:
  session_start:
    - command: %q
      type: command
`, j000400ContextMarker, j000400CommandMarker, j000400MCPCommand, j000400HookCommand)
}

func registerJ000400Steps(ctx *godog.ScenarioContext) {
	// --- Outline A: materialization (hermetic) --------------------------------

	ctx.Step(`^Carol's team profile carries a shared fragment, command, MCP server, and hook$`, func(c context.Context) error {
		w := worldFrom(c)
		j000400Of(w)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", "version: 4\n"); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/content/bundles/team.yaml", j000400TeamBundleYAML()); err != nil {
			return err
		}
		return runOK(w, "profile", "create", "team", "-b", "team", "-d", "J000400 shared team profile")
	})

	ctx.Step(`^Alice materializes the team profile for (\S+)$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		j000400 := j000400Of(w)
		j000400.engine = engine
		j000400.target = j000400TargetFor(engine)
		return runOK(w, "profile", "materialize", "team", "--target", j000400.target, "--backend", engine)
	})

	ctx.Step(`^the materialized (\S+) context carries the shared fragment's marker, in its own native shape$`, func(c context.Context, engine string) error {
		return j000400AssertContext(worldFrom(c), engine)
	})

	ctx.Step(`^the materialized (\S+) MCP configuration carries the shared server's command, in its own native shape$`, func(c context.Context, engine string) error {
		return j000400AssertMCP(worldFrom(c), engine)
	})

	ctx.Step(`^the materialized (\S+) MCP configuration invokes ctxloom's own server as "([^"]*)"$`, func(c context.Context, engine, want string) error {
		return j000400AssertCtxloomMCPInvocation(worldFrom(c), engine, want)
	})

	ctx.Step(`^the materialized (\S+) hook configuration carries the shared hook's command, in its own native shape$`, func(c context.Context, engine string) error {
		return j000400AssertHook(worldFrom(c), engine)
	})

	ctx.Step(`^the materialized (\S+) command file carries the shared command's body, in its own native shape$`, func(c context.Context, engine string) error {
		return j000400AssertCommand(worldFrom(c), engine)
	})

	// --- U3: per-engine capability LOSS, asserted as a payload absence -----
	//
	// The inverse of the four "carries ... in its own native shape" steps: it
	// walks EVERY file the materialize wrote and proves the hook command is
	// in none of them. Deliberately not a "the file X does not contain"
	// assertion against one guessed path — an absence claim is only worth
	// something if it covers the whole output tree, since the interesting
	// failure mode is "it landed somewhere I did not think to look".
	ctx.Step(`^no (\S+) surface anywhere in the materialized tree carries the shared hook's command$`,
		func(c context.Context, engine string) error {
			return j000400AssertNoHookAnywhere(worldFrom(c), engine)
		})

	// The other half of the same finding, and the one that is @wip: the loss
	// above is STRUCTURAL and fine to have, but silent. This asserts the
	// materialize report says so.
	ctx.Step(`^the materialize report names the hook it could not deliver to (\S+)$`,
		func(c context.Context, engine string) error {
			w := worldFrom(c)
			out := w.env.LastOutput()
			w.docStepMaterialized = fmt.Sprintf("materialize report for %s:\n%s", engine, out)
			if !strings.Contains(strings.ToLower(out), "hook") {
				return fmt.Errorf("the %s materialize report never mentions hooks at all, so a team inheriting this profile is never told its guardrails did not come with it; report:\n%s", engine, out)
			}
			return nil
		})

	// --- Regression: hand-authored content survives (P0 data loss) -----------
	//
	// A hand-authored context file must survive materialization byte-for-byte
	// outside ctxloom's managed markers. The hand-authored step writes directly
	// into the SAME target dir "Alice materializes the team profile for
	// <engine>" derives deterministically ("out-"+engine), so it must run
	// BEFORE that step in the Gherkin — Given/And ordering, not execution order
	// inferred from step registration.

	ctx.Step(`^Alice's team already hand-authored (\S+) for (\S+) with their own conventions$`, func(c context.Context, file, engine string) error {
		w := worldFrom(c)
		j000400 := j000400Of(w)
		// This hand-authored step MUST run before "Alice
		// materializes the team profile for <engine>" for the byte-for-byte
		// regression it sets up to mean anything -- previously enforced only
		// by a comment ("Given/And ordering, not execution order inferred
		// from step registration"). Failing loudly here if a materialize
		// already ran turns that ordering requirement into something the
		// suite itself catches rather than something only a careful reader
		// of the Gherkin would notice was violated.
		if j000400.target != "" {
			return fmt.Errorf("j000400: hand-authoring %s for %s after a materialize already ran (target %q already set) -- this step must come BEFORE \"Alice materializes the team profile for %s\" in the Gherkin", file, engine, j000400.target, engine)
		}
		j000400.engine = engine
		j000400.target = j000400TargetFor(engine)
		rel := filepath.Join(j000400.target, file)
		// Deliberately more than one line, and deliberately including prose
		// that carries no marker of its own: the P0 this scenario guards
		// (lanky-plop) is CONTENT LOSS, and content loss does not politely
		// confine itself to the one line a test happens to grep for. The
		// heading and the two comment lines are exactly what a merge bug
		// dropped while leaving the marker line intact.
		j000400.handAuthoredBytes = "# Team conventions\n\n" +
			"<!-- Alice wrote this by hand; ctxloom must never touch it -->\n" +
			j000400HandAuthoredMarker + "\n\n" +
			"## House style\n\n- tabs, not spaces\n- no trailing whitespace\n"
		return w.env.WriteFile(rel, j000400.handAuthoredBytes)
	})

	// BYTE-FOR-BYTE, measured. This used to be one strings.Contains for one
	// marker line, which is not what the scenario's own title claims and not
	// what the P0 it guards was about: a mutation that made
	// agent.WriteManagedContext silently drop every hand-authored comment line
	// from the pre-marker region — the exact data-loss class — left all six
	// j000400 scenarios green while "# Team conventions" vanished unnoticed.
	//
	// The comparison is over the file's NON-MANAGED regions (everything
	// outside ctxloom's own markers), because those are precisely the bytes
	// ctxloom promises not to touch. The managed section itself is asserted by
	// the step that follows this one in the Gherkin.
	ctx.Step(`^(\S+) still carries Alice's hand-authored conventions, byte-for-byte$`, func(c context.Context, file string) error {
		w := worldFrom(c)
		j000400 := j000400Of(w)
		if j000400.handAuthoredBytes == "" {
			return fmt.Errorf("j000400: no hand-authored snapshot recorded — the hand-authoring step must run before this one")
		}
		rel := filepath.Join(j000400.target, file)
		got, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		if !strings.Contains(got, j000400ManagedBegin) {
			return fmt.Errorf("%s carries no ctxloom managed section at all, so nothing was materialized alongside the hand-authored content; content:\n%s", rel, got)
		}
		outside := j000400StripManagedSection(got)
		if outside != j000400.handAuthoredBytes {
			return fmt.Errorf("%s: the region outside ctxloom's managed markers is NOT byte-identical to what Alice hand-authored.\n--- hand-authored (%d bytes) ---\n%q\n--- survived (%d bytes) ---\n%q\n--- whole file after materialize ---\n%s",
				rel, len(j000400.handAuthoredBytes), j000400.handAuthoredBytes, len(outside), outside, got)
		}
		w.docStepMaterialized = rel + " (hand-authored region, byte-identical after materialize):\n" + outside
		return nil
	})

	// --- Outline B: live (@live) -------------------
	//
	// "a real (\S+) agent is available" is steps_live.go's shared gate step,
	// reused VERBATIM (godog rejects an ambiguous second match for the same
	// step text — steps_j001500.go:229-235) — it self-skips the whole scenario
	// (godog.ErrSkip) without credentials, exactly as distill_live.feature's
	// own Scenario Outlines already rely on.

	ctx.Step(`^Carol's team profile carries a fragment with a sentinel marker$`, func(c context.Context) error {
		w := worldFrom(c)
		body := fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  marker:\n    content: %q\n", j000400LiveSentinel)
		if err := w.env.WriteFile(".ctxloom/content/bundles/team.yaml", body); err != nil {
			return err
		}
		return runOK(w, "profile", "create", "team", "-b", "team", "-d", "J000400 shared team profile")
	})

	ctx.Step(`^Alice asks her (\S+) assistant to repeat the sentinel it can see$`, func(c context.Context, _ string) error {
		w := worldFrom(c)
		_ = w.env.Run("run", "--one-shot", "--profile", "team",
			"Please repeat, verbatim and in full, the one distinct marker phrase you can see in your context. Output nothing else.")
		return nil
	})

	ctx.Step(`^its reply contains the sentinel marker$`, func(c context.Context) error {
		w := worldFrom(c)
		if !strings.Contains(w.env.LastOutput(), j000400LiveSentinel) {
			return fmt.Errorf("assistant reply does not contain the sentinel marker; output:\n%s", w.env.LastOutput())
		}
		return nil
	})
}

// j000400FileContains reads a project-relative file and asserts it contains want.
func j000400FileContains(w *World, rel, want string) error {
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
	w.docStepMaterialized = fmt.Sprintf("%s:\n%s", rel, j000400Excerpt(body, want, 1))
	if !strings.Contains(body, want) {
		return fmt.Errorf("file %q does not contain %q; content:\n%s", rel, want, body)
	}
	return nil
}

// j000400Excerpt returns a short window of body centered on the first line
// containing marker: up to context lines before and after. Falls back to the
// full (trimmed) body if marker never appears on its own line, so a caller
// never loses evidence to a formatting surprise.
func j000400Excerpt(body, marker string, context int) string {
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
// context surface (internal/{claude,codex,kiro}/surfaces.go).
// Shared engine-axis knowledge: J000400's own materialization outline uses it
// below, and J000800's onboarding journey reuses it rather than re-deriving a
// second copy of the same per-engine path table (steps_j000800_onboarding.go's
// "Bob starts a session on <engine>" outline). codex is now a case here:
// `profile materialize` used to leave its context surface a silent no-op
// (keyed on agent.SurfaceInputs.Fragments, which materialize never
// populates); codex's context surface now ALSO writes
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
	case "opencode":
		// opencode's ctxloom-owned context file, referenced from
		// opencode.json's `instructions` key (internal/opencode's
		// contextSurface / OpencodeWriter.WriteContext).
		return filepath.Join(dir, ".opencode", "ctxloom-context.md"), nil
	default:
		return "", fmt.Errorf("unknown engine %q for native context surface", engine)
	}
}

// j000400AssertContext dispatches to the right native context surface per engine.
func j000400AssertContext(w *World, engine string) error {
	j000400 := j000400Of(w)
	rel, err := engineContextRelPath(j000400.target, engine)
	if err != nil {
		return err
	}
	return j000400FileContains(w, rel, j000400ContextMarker)
}

// j000400ReadJSON reads a project-relative file and parses it as a generic JSON
// document.
func j000400ReadJSON(w *World, rel string) (map[string]any, error) {
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

// j000400ReadTOML reads a project-relative file and parses it as a generic TOML
// document (codex's .codex/config.toml).
func j000400ReadTOML(w *World, rel string) (map[string]any, error) {
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

// j000400AssertMCP parses each engine's MCP registry in its own native format
// (JSON's shared "mcpServers" table for claude/kiro — the same
// agent.MCPFileConfig reconciler backs both — or codex's TOML
// "mcp_servers" table folded into config.toml) and asserts the shared
// server's command landed under its name.
func j000400AssertMCP(w *World, engine string) error {
	rel, key, err := j000400MCPRegistryFor(j000400Of(w).target, engine)
	if err != nil {
		return err
	}
	doc, err := j000400ReadMCPRegistry(w, rel)
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
	cmd := j000400ServerCommand(srv)
	// Surface the real parsed server entry — the actual command (and args, if
	// any) this engine's OWN file carries under its own table name (mcpServers
	// vs codex's mcp_servers) — to the @doc capture sidecar (set-and-consume;
	// no-op when capture is off), rather than letting the published page show
	// only a bare claim.
	evidence := fmt.Sprintf("%s → %s.toolserver\n  command: %s", rel, key, cmd)
	if args := j000400FormatArgs(srv["args"]); args != "" {
		evidence += fmt.Sprintf("\n  args:    %s", args)
	}
	w.docStepMaterialized = evidence
	if cmd != j000400MCPCommand {
		return fmt.Errorf("%s's MCP server %q has command %q, want %q", engine, "toolserver", cmd, j000400MCPCommand)
	}
	return nil
}

// j000400AssertCtxloomMCPInvocation reads the engine's OWN materialized MCP
// registry and asserts the subcommand ctxloom's auto-registered entry invokes.
//
// It is the argv, not the entry's presence, that decides whether the engine
// gets ctxloom's tools: an entry naming a spelling that does not speak the
// protocol produces a session that starts perfectly and has no ctxloom tools
// in it. Every existence check in this suite passes against exactly that.
//
// The two native shapes are both the engine's own: most registries carry a
// string `command` plus an `args` array, while opencode folds the binary and
// its arguments into ONE `command` array.
func j000400AssertCtxloomMCPInvocation(w *World, engine, want string) error {
	rel, key, err := j000400MCPRegistryFor(j000400Of(w).target, engine)
	if err != nil {
		return err
	}
	doc, err := j000400ReadMCPRegistry(w, rel)
	if err != nil {
		return err
	}
	top, ok := doc[key].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: no %q table in the generated MCP configuration; parsed: %+v", engine, key, doc)
	}
	srv, ok := top[agent.MCPServerName].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: no %q server entry under %q, so this engine's session gets none of ctxloom's tools; parsed: %+v", engine, agent.MCPServerName, key, top)
	}

	var tokens []string
	if args := j000400FormatArgs(srv["args"]); args != "" {
		tokens = strings.Fields(strings.Trim(args, "[]"))
	} else if argv, ok := srv["command"].([]any); ok && len(argv) > 1 {
		tokens = strings.Fields(strings.Trim(j000400FormatArgs(argv[1:]), "[]"))
	}
	got := strings.Join(tokens, " ")

	w.docStepMaterialized = fmt.Sprintf("%s → %s.%s\n  command: %s\n  invokes: ctxloom %s",
		rel, key, agent.MCPServerName, j000400ServerCommand(srv), got)
	if got != want {
		return fmt.Errorf("%s's ctxloom MCP entry invokes %q, want %q — an engine launching it would come up with no ctxloom tools and nothing saying why", engine, got, want)
	}
	return nil
}

// j000400MCPRegistryFor names the file an engine keeps its MCP registry in, and the
// table key inside it. claude-code and kiro share JSON's "mcpServers" (one
// agent.MCPFileConfig reconciler backs both); codex folds "mcp_servers"
// into the same config.toml its hooks live in; opencode folds "mcp" into the
// same opencode.json its `instructions` context reference lives in.
func j000400MCPRegistryFor(dir, engine string) (rel, key string, err error) {
	switch engine {
	case "claude-code":
		return filepath.Join(dir, ".mcp.json"), "mcpServers", nil
	case "kiro":
		return filepath.Join(dir, ".kiro", "settings", "mcp.json"), "mcpServers", nil
	case "codex":
		return filepath.Join(dir, ".codex", "config.toml"), "mcp_servers", nil
	case "opencode":
		return filepath.Join(dir, "opencode.json"), "mcp", nil
	default:
		return "", "", fmt.Errorf("j000400: unknown engine %q", engine)
	}
}

// j000400ReadMCPRegistry decodes an MCP registry file by its extension — TOML for
// codex's config.toml, JSON for everyone else — so the caller does not repeat
// the format choice alongside the path choice.
func j000400ReadMCPRegistry(w *World, rel string) (map[string]any, error) {
	if filepath.Ext(rel) == ".toml" {
		return j000400ReadTOML(w, rel)
	}
	return j000400ReadJSON(w, rel)
}

// j000400ServerCommand reads one MCP server entry's executable, tolerating the two
// native shapes in play. Most engines carry a "command" STRING beside a
// separate "args" list; opencode carries a single "command" ARRAY whose first
// element is the binary. Reading both keeps the per-engine dispatch honest
// about each engine's OWN idiom rather than asserting a shape it does not use.
func j000400ServerCommand(srv map[string]any) string {
	if cmd, ok := srv["command"].(string); ok && cmd != "" {
		return cmd
	}
	if argv, ok := srv["command"].([]any); ok && len(argv) > 0 {
		first, _ := argv[0].(string)
		return first
	}
	return ""
}

// j000400FormatArgs renders a decoded JSON/TOML args array (a []any of strings) as
// a short bracketed, space-joined native-ish snippet for evidence display, or
// "" if there are none.
func j000400FormatArgs(v any) string {
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

// j000400HookCommandsFrom walks a decoded hooks event value — which is, depending
// on the engine, either a flat list of {matcher, command} entries (kiro) or a
// list of {matcher, hooks: [{type, command}]} groups (claude, codex) — and
// collects every command found, so one walker serves every engine's own
// shape.
func j000400HookCommandsFrom(v any) []string {
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
			out = append(out, j000400HookCommandsFrom(nested)...)
		}
	}
	return out
}

// j000400AssertHook parses each engine's own hook configuration (claude's
// .claude/settings.json "hooks.SessionStart", kiro's agent JSON
// "hooks.agentSpawn" — kiro diverts session_start to its own event name —
// codex's config.toml "hooks.SessionStart" folded into the same file MCP
// lives in) and asserts the shared hook's command landed under the right
// event.
func j000400AssertHook(w *World, engine string) error {
	j000400 := j000400Of(w)
	dir := j000400.target
	var (
		doc   map[string]any
		event string
		err   error
		rel   string
	)
	switch engine {
	case "claude-code":
		rel = filepath.Join(dir, ".claude", "settings.json")
		doc, err = j000400ReadJSON(w, rel)
		event = "SessionStart"
	case "kiro":
		rel = filepath.Join(dir, ".kiro", "agents", "ctxloom.json")
		doc, err = j000400ReadJSON(w, rel)
		event = "agentSpawn"
	case "codex":
		rel = filepath.Join(dir, ".codex", "config.toml")
		doc, err = j000400ReadTOML(w, rel)
		event = "SessionStart"
	default:
		return fmt.Errorf("j000400: unknown engine %q", engine)
	}
	if err != nil {
		return err
	}
	top, ok := doc["hooks"].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: no %q table in the generated hook configuration; parsed: %+v", engine, "hooks", doc)
	}
	cmds := j000400HookCommandsFrom(top[event])
	found := false
	for _, cmd := range cmds {
		if cmd == j000400HookCommand {
			found = true
			break
		}
	}
	// Surface the real event name and command this engine's own hook
	// configuration carries to the @doc capture sidecar (set-and-consume;
	// no-op when capture is off) — kiro diverts session_start to its own
	// "agentSpawn" event name, which the note below makes visible rather than
	// leaving it as a fact only findable by re-reading the Go source.
	//
	// This used to print j000400HookCommand (the WANT, restating the
	// claim) rather than cmds (what was actually parsed out of the generated
	// file) — so a failing assertion published evidence that looked
	// identical whether the real command matched or not. Now prints cmds,
	// matching j000400AssertMCP's own pattern.
	note := ""
	if engine == "kiro" {
		note = "  (kiro diverts session_start → agentSpawn, its own event name)"
	}
	w.docStepMaterialized = fmt.Sprintf("%s → hooks.%s%s\n  commands: %v", rel, event, note, cmds)
	if found {
		return nil
	}
	return fmt.Errorf("%s's %q hooks %v do not include the shared hook's command %q", engine, event, cmds, j000400HookCommand)
}

// j000400AssertCommand asserts the shared command's body reached each engine's own
// command file path — claude/codex flatten the "<bundle>/<item>" export name's
// slash to a dash (backends/commandfiles.go's exportNames), while kiro
// preserves it as a subdirectory (internal/kiro/capabilities.go), rendering
// commands as `<name>/SKILL.md` directories.
// j000400AssertNoHookAnywhere proves a capability LOSS on the payload: it walks the
// whole materialized tree and fails if ANY file carries the shared hook's
// command. Used by U3's opencode row, where hooks are structurally absent —
// internal/opencode's NewSurfaces dispatch table registers context, settings
// (MCP folded in), commands and skills, and no hook surface at all.
//
// It also guards the opposite regression: should opencode ever GAIN a hook
// surface, this goes red and whoever adds it is pointed straight at the
// journey scenario whose premise their change invalidates.
func j000400AssertNoHookAnywhere(w *World, engine string) error {
	j000400 := j000400Of(w)
	root := filepath.Join(w.env.ProjectDir, j000400.target)
	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(body), j000400HookCommand) {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("j000400: walking the materialized %s tree (%s): %w", engine, j000400.target, err)
	}
	if len(found) > 0 {
		return fmt.Errorf("expected %s to carry NO hook surface, but the shared hook's command appears in: %s -- if %s genuinely gained a hook mechanism, this journey's premise changed and the scenario needs rewriting, not silencing", engine, strings.Join(found, ", "), engine)
	}
	w.docStepMaterialized = fmt.Sprintf("%s: walked the whole materialized tree; the shared hook's command appears in no file", engine)
	return nil
}

func j000400AssertCommand(w *World, engine string) error {
	j000400 := j000400Of(w)
	dir := j000400.target
	var rel string
	switch engine {
	case "claude-code":
		rel = filepath.Join(dir, ".claude", "commands", "team-onboarding.md")
	case "codex":
		rel = filepath.Join(dir, ".codex", "prompts", "team-onboarding.md")
	case "kiro":
		rel = filepath.Join(dir, ".kiro", "skills", "team", "onboarding", "SKILL.md")
	case "opencode":
		rel = filepath.Join(dir, ".opencode", "command", "team", "onboarding.md")
	default:
		return fmt.Errorf("j000400: unknown engine %q", engine)
	}
	return j000400FileContains(w, rel, j000400CommandMarker)
}
