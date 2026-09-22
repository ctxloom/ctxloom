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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cucumber/godog"
	"github.com/pelletier/go-toml/v2"

	"github.com/ctxloom/ctxloom/internal/core/agent"

	"github.com/ctxloom/ctxloom/internal/core/config"
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
	engine string // current Examples row's backend name (claude-code/codex)
	target string // profile materialize --target dir for this row (relative to project root)

	// handAuthoredBytes is the EXACT content the hand-authored fixture wrote
	// before any materialize ran. The byte-for-byte regression compares the
	// post-materialize file's non-managed regions against this snapshot, so
	// "byte-for-byte" in the scenario title is measured rather than sampled.
	handAuthoredBytes string
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
		if err := w.env.WriteFile(".ctxloom/config.yaml", fmt.Sprintf("version: %d\n", config.CurrentConfigVersion)); err != nil {
			return err
		}
		if err := w.env.WriteFile(bundleFilePath("team"), j000400TeamBundleYAML()); err != nil {
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

	ctx.Step(`^the materialized (\S+) MCP configuration registers no server under ctxloom's own name$`, func(c context.Context, engine string) error {
		return j000400AssertNoCtxloomMCPEntry(worldFrom(c), engine)
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
	// The artifact is a PARAMETER because two different absences now need this
	// shape: opencode's structural hook gap, and codex's declared absence of a
	// durable project home for its settings, MCP servers and prompts. Three
	// hand-written walks over one tree would drift.
	ctx.Step(`^no (\S+) surface anywhere in the materialized tree carries the shared (hook's command|MCP server's command|command's body)$`,
		func(c context.Context, engine, artifact string) error {
			return j000400AssertMarkerNowhere(worldFrom(c), engine, artifact)
		})

	// codex's declared absence, asserted on the REPORT rather than the tree
	// (the tree half is the step above). A materialize that quietly writes four
	// true "wrote" lines and says nothing about the settings, MCP servers,
	// prompts and skills that went nowhere is this project's signature failure
	// — the loss has to be stated in the same report as the wins.
	// Neither call site (cli/mcp.feature, cli/manage.feature,
	// journeys/j000400_multi_engine.feature) asks for a format, so off a
	// terminal (this harness, always) they are all exercising the SAME
	// derived-default row: `profile materialize` is wired to emit(), and its
	// JSON MaterializeProfileResult carries the loss in a structured
	// not_carried array (agent.SurfaceLoss{surface,detail,reason}) rather than
	// the "NOT carried: ..." text line this used to scan for. Branching here
	// on the format actually resolved fixes all three call sites without
	// threading a <flags> table through scenarios that were never about
	// --format in the first place.
	ctx.Step(`^the materialize report names each surface (\S+) does not carry, with a reason$`,
		func(c context.Context, engine string) error {
			w := worldFrom(c)
			out := w.env.LastStdout()
			w.docStepMaterialized = fmt.Sprintf("materialize report for %s:\n%s", engine, out)
			// The engine's declaration is the whole reason a kind is absent
			// (delivery.ErrUncarried carries no reason string): the report
			// names the surface and says the engine declares no approach
			// for it, so a reader can tell a declared absence from a bug.
			want := []string{"mcp", "hooks", "commands"}
			if !formatAskedFor(w).Structured() {
				if !strings.Contains(out, "NOT carried") {
					return fmt.Errorf("the %s materialize report does not list the undelivered surfaces at all; report:\n%s", engine, out)
				}
				for _, surface := range want {
					if !strings.Contains(out, surface) {
						return fmt.Errorf("the %s materialize report does not name %s among the surfaces it did not carry; report:\n%s", engine, surface, out)
					}
				}
				return nil
			}
			losses, err := lastOutputJSONArray(w, "not_carried")
			if err != nil {
				return fmt.Errorf("%v; stdout:\n%s", err, w.env.LastStdout())
			}
			named := map[string]bool{}
			for _, loss := range losses {
				surface, err := jsonAtPath(loss, "surface")
				if err != nil {
					continue
				}
				reason, err := jsonAtPath(loss, "reason")
				if err != nil {
					return fmt.Errorf("a not_carried entry states no reason; stdout:\n%s", w.env.LastStdout())
				}
				if got, ok := jsonScalar(reason); !ok || strings.TrimSpace(got) == "" {
					return fmt.Errorf("a not_carried entry states an empty reason; stdout:\n%s", w.env.LastStdout())
				}
				if got, ok := jsonScalar(surface); ok {
					named[got] = true
				}
			}
			for _, surface := range want {
				if !named[surface] {
					return fmt.Errorf("the %s materialize JSON report's not_carried array does not name %q; stdout:\n%s", engine, surface, w.env.LastStdout())
				}
			}
			return nil
		})

	// The other half of the same finding, and the one that is @wip: the loss
	// above is STRUCTURAL and fine to have, but silent. This asserts the
	// materialize report says so.
	ctx.Step(`^the materialize report names the hook it could not deliver to (\S+)$`,
		func(c context.Context, engine string) error {
			w := worldFrom(c)
			out := w.env.LastStdout()
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
		// is CONTENT LOSS, and content loss does not politely
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
		// The context is APPENDED after the hand-authored bytes (a blank
		// line between); the ownership record, not a marker section, owns
		// what was appended. What Alice wrote is the file's prefix, byte
		// for byte.
		authored := strings.TrimRight(j000400.handAuthoredBytes, "\n")
		if !strings.HasPrefix(got, authored) {
			return fmt.Errorf("%s: what Alice hand-authored is NOT the file's byte-identical prefix after materialize.\n--- hand-authored (%d bytes) ---\n%q\n--- whole file after materialize ---\n%s",
				rel, len(j000400.handAuthoredBytes), j000400.handAuthoredBytes, got)
		}
		if strings.TrimSpace(strings.TrimPrefix(got, authored)) == "" {
			return fmt.Errorf("%s carries nothing beyond the hand-authored content, so nothing was materialized alongside it; content:\n%s", rel, got)
		}
		w.docStepMaterialized = rel + " (hand-authored prefix, byte-identical after materialize):\n" + authored
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
		if err := w.env.WriteFile(bundleFilePath("team"), body); err != nil {
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
// context surface.
// Shared engine-axis knowledge: J000400's own materialization outline uses it
// below, and J000800's onboarding journey reuses it rather than re-deriving a
// second copy of the same per-engine path table (steps_j000800_onboarding.go's
// "Bob starts a session on <engine>" outline).
func engineContextRelPath(dir, engine string) (string, error) {
	switch engine {
	case "claude-code":
		return filepath.Join(dir, "CLAUDE.md"), nil
	// All three doubles share mock's native CONTEXT layout, and each differs
	// elsewhere: mock-lossy in the hook kinds its descriptor declares
	// unsupported, mock-launch in delivering only context at materialize time
	// (its other four surfaces arrive at launch). Context is the one surface
	// all three write, which is why one case covers them.
	case config.BackendMock, config.BackendMockLossy, config.BackendMockLaunch:
		return filepath.Join(dir, "MOCK_CONTEXT.md"), nil
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
// (JSON's "mcpServers" table for claude — or codex's TOML
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

// j000400AssertNoCtxloomMCPEntry reads the engine's registry in its own
// native shape and pins that no entry sits under ctxloom's own name: a
// registry materialized at rest carries the bundle's shared servers and
// nothing for ctxloom, whose server the session injects (URL + bearer) into
// its own registry at session start.
func j000400AssertNoCtxloomMCPEntry(w *World, engine string) error {
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
	w.docStepMaterialized = fmt.Sprintf("%s → %s: servers %v", rel, key, slices.Sorted(maps.Keys(top)))
	if srv, present := top[agent.MCPServerName]; present {
		return fmt.Errorf("%s's materialized registry carries an entry under %q (%v): ctxloom's own server is served by the running session's endpoint and is never materialized at rest — an engine launching this entry would come up with no ctxloom tools and nothing saying why", engine, agent.MCPServerName, srv)
	}
	return nil
}

// j000400MCPRegistryFor names the file an engine keeps its MCP registry in, and the
// table key inside it. claude-code uses JSON's "mcpServers"
// (agent.MCPFileConfig's reconciler); codex folds "mcp_servers"
// into the same config.toml its hooks live in; opencode folds "mcp" into the
// same opencode.json its `instructions` context reference lives in.
//
// codex has NO ROW, and its absence is the declared one: its MCP servers fold
// into $CODEX_HOME/config.toml, a file no harpless materialize can name
// (internal/codex/declared_absence.go). Asking for one here is a scenario bug —
// the codex claim is an ABSENCE claim, asserted by
// j000400AssertMarkerNowhere over the whole tree — so the error says so rather
// than handing back a path nothing will ever be at.
func j000400MCPRegistryFor(dir, engine string) (rel, key string, err error) {
	switch engine {
	case "claude-code":
		return filepath.Join(dir, ".mcp.json"), "mcpServers", nil
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
// on the engine, either a flat list of {matcher, command} entries or a
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
// .claude/settings.json "hooks.SessionStart",
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
	// no-op when capture is off).
	//
	// This used to print j000400HookCommand (the WANT, restating the
	// claim) rather than cmds (what was actually parsed out of the generated
	// file) — so a failing assertion published evidence that looked
	// identical whether the real command matched or not. Now prints cmds,
	// matching j000400AssertMCP's own pattern.
	w.docStepMaterialized = fmt.Sprintf("%s → hooks.%s\n  commands: %v", rel, event, cmds)
	if found {
		return nil
	}
	return fmt.Errorf("%s's %q hooks %v do not include the shared hook's command %q", engine, event, cmds, j000400HookCommand)
}

// j000400AssertCommand asserts the shared command's body reached each engine's own
// command file path — claude/codex flatten the "<bundle>/<item>" export name's
// slash to a dash (backends/commandfiles.go's exportNames).
// j000400LostArtifacts maps the Gherkin phrase naming a shared artifact to the
// sentinel that proves it landed. One map, so an absence claim and its matching
// presence claim can never be about different bytes.
var j000400LostArtifacts = map[string]string{
	"hook's command":       j000400HookCommand,
	"MCP server's command": j000400MCPCommand,
	"command's body":       j000400CommandMarker,
}

// j000400AssertMarkerNowhere proves a delivery LOSS on the payload: it walks the
// whole materialized tree and fails if ANY file carries the named artifact.
//
// Two different absences use it, for two different reasons, and the difference
// matters to whoever it goes red on:
//
//   - opencode's hooks are STRUCTURALLY absent — internal/opencode's
//     NewSurfaces registers context, settings (MCP folded in), commands and
//     skills, and no hook surface at all.
//   - codex's settings, MCP servers and prompts are a DECLARED ABSENCE on this
//     harpless path: they exist only inside a per-session engine home
//     (internal/codex/declared_absence.go), so a static materialize has nowhere
//     to put them.
//
// It also guards the opposite regression both ways: should opencode gain a hook
// surface, or codex regrow a durable project home, this goes red and whoever
// did it is pointed at the scenario whose premise their change invalidates.
func j000400AssertMarkerNowhere(w *World, engine, artifact string) error {
	marker, ok := j000400LostArtifacts[artifact]
	if !ok {
		return fmt.Errorf("j000400: no sentinel known for the shared %s", artifact)
	}
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
		if strings.Contains(string(body), marker) {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("j000400: walking the materialized %s tree (%s): %w", engine, j000400.target, err)
	}
	if len(found) > 0 {
		return fmt.Errorf("expected %s to carry the shared %s NOWHERE, but it appears in: %s -- if %s genuinely gained that surface, this journey's premise changed and the scenario needs rewriting, not silencing", engine, artifact, strings.Join(found, ", "), engine)
	}
	w.docStepMaterialized = fmt.Sprintf("%s: walked the whole materialized tree; the shared %s appears in no file", engine, artifact)
	return nil
}

func j000400AssertCommand(w *World, engine string) error {
	j000400 := j000400Of(w)
	dir := j000400.target
	var rel string
	switch engine {
	case "claude-code":
		rel = filepath.Join(dir, ".claude", "commands", "team-onboarding.md")
	default:
		return fmt.Errorf("j000400: unknown engine %q", engine)
	}
	return j000400FileContains(w, rel, j000400CommandMarker)
}
