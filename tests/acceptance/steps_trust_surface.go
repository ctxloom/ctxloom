//go:build acceptance

// The trust-surface matrix (trust_surface.feature): a REFERENCE demonstration,
// not a journey — no persona arc, just the exhaustive element × decision table
// this file proves. One bundle ships one of each trust-addressable kind
// (fragment, skill, mcp server, hook); each row approves or rejects ONE of
// them and asserts the PAYLOAD — the element's actual bytes in the generated
// surface — landed or did not.
//
// TWO fixtures, matching the feature file's two Outlines, and deliberately
// not one: a denial that starts from a state the item was ALREADY withheld
// from (unsigned, never reviewed) can never fail for the reason it claims to
// prove — rejecting it changes nothing observable, so a broken reject path
// would still pass. Denial is proven from a bundle a TRUSTED publisher
// signed (allowed by default, exactly J3's Background); approval is proven
// from an unsigned bundle (denied by default). Each Outline's Given step
// seeds the fixture that makes ITS verb meaningful.
//
// Reuses steps_j1_common.go's ensureProjectWithEngine/runOK, steps_j3.go's
// signed-remote pattern (testenv.GenerateTestSigner/SeedSignedRemote/
// TrustSigner), and steps_j5.go's JSON/hook-command parsing helpers
// (j5ReadJSON, j5HookCommandsFrom, j5FormatArgs) rather than re-deriving
// them — same package, same conventions. "Alice starts a session" is
// steps_j1_setup.go's existing step (materialize "default" into "out"),
// reused VERBATIM.
package acceptance

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// Distinctive marker strings this feature's bundle carries, so the generated
// surface can be checked for exactly the right payload — never a bare
// file-exists or exit-code proxy.
const (
	tsFragmentMarker = "TS-FRAGMENT-MARKER-4b7f1a"
	tsSkillMarker    = "TS-SKILL-MARKER-9d2e60"
	tsMCPMarker      = "TS-MCP-EXEC-MARKER-6a1c33"
	tsHookMarker     = "echo TS-HOOK-EXEC-MARKER-7f0e52"
)

// tsState is this feature's fixture state: the seeded remote's URL, the
// bundle name inside it, and (signed-fixture only) the trusted principal.
type tsState struct {
	url        string
	bundleName string
}

func tsOf(w *World) *tsState {
	if w.ts == nil {
		w.ts = &tsState{bundleName: "trustdemo"}
	}
	return w.ts
}

// tsBundleYAML renders the one bundle both fixtures share: a fragment, a
// skill, an MCP server, a hook, and a profile — one of each trust-addressable
// kind, plus the one kind that ISN'T (profiles) — each carrying its own
// distinctive marker. Identical content either signed or not; only the
// SEEDING (signed+trusted vs unsigned) differs between the two fixtures.
func tsBundleYAML() string {
	return fmt.Sprintf(`version: "1.0.0"
fragments:
  context:
    content: %q
skills:
  guide:
    description: trust-surface demo skill
    content: %q
mcp:
  toolserver:
    command: "/bin/echo"
    args: [%q]
hooks:
  session_start:
    - command: %q
      type: command
profiles:
  reviewme:
    description: trust-surface demo profile — never trust-gated, see the last scenario
`, tsFragmentMarker, tsSkillMarker, tsMCPMarker, tsHookMarker)
}

// tsRef composes the canonical item ref for this feature's seeded bundle:
// "file://<bare>@bundles/<bundleName>#<selector>", the same shape
// steps_j3.go/steps_review.go drive `ctxloom trust`/`ctxloom blacklist` with.
func tsRef(w *World, selector string) string {
	ts := tsOf(w)
	return ts.url + "@bundles/" + ts.bundleName + "#" + selector
}

// tsSelector maps a Scenario Outline's <element> column to the item's
// "<kind>/<name>" selector (internal/operations/trust.go's parseTrustSelector
// vocabulary: fragments|skills|mcp|hooks).
func tsSelector(element string) (string, error) {
	switch element {
	case "fragment":
		return "fragments/context", nil
	case "skill":
		return "skills/guide", nil
	case "MCP server":
		return "mcp/toolserver", nil
	case "hook":
		return "hooks/session_start/0", nil
	default:
		return "", fmt.Errorf("trust-surface: unknown element %q", element)
	}
}

// tsWireAndPull adds the seeded remote and references its bundle from the
// "default" profile — the same reference-then-pull mechanic every other
// journey's sources use (steps_j1_common.go's addSourceAsRemote, steps_j3.go's
// j3WireReference) — then pulls, so the freshly seeded content resolves
// through the SeededBundleLoader before any trust/blacklist/materialize call.
func tsWireAndPull(w *World, url string) error {
	ts := tsOf(w)
	ts.url = url
	if err := runOK(w, "remote", "add", "trustdemo", url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "trustdemo/"+ts.bundleName); err != nil {
		return err
	}
	return runOK(w, "remote", "pull")
}

func registerTrustSurfaceSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a skill, an MCP server, and a hook$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		ts := tsOf(w)
		rel := ".ctxloom/content/bundles/" + ts.bundleName + ".yaml"
		// Deliberately UNSIGNED: every item is born pending (denied by
		// default), so approving one is the only thing that can expose it —
		// the meaningful state for the APPROVE outline (see file doc).
		url, err := w.env.SeedRemote(map[string]string{rel: tsBundleYAML()})
		if err != nil {
			return fmt.Errorf("seed unsigned trust-surface remote: %w", err)
		}
		return tsWireAndPull(w, url)
	})

	ctx.Step(`^a trusted publisher's signed bundle ships one of each: a fragment, a skill, an MCP server, and a hook$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		ts := tsOf(w)
		rel := ".ctxloom/content/bundles/" + ts.bundleName + ".yaml"
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate trust-surface signer: %w", err)
		}
		url, err := w.env.SeedSignedRemote(map[string]string{rel: tsBundleYAML()}, []string{rel}, signer)
		if err != nil {
			return fmt.Errorf("seed signed trust-surface remote: %w", err)
		}
		// Trust the key BEFORE wiring/pulling: every item is allowed by
		// default (trusted-signer, EffectiveTrust step 5) — the meaningful
		// state for the REJECT outline (see file doc): rejecting one item
		// must beat an allow that is already in effect, exactly as J3 proves
		// for hooks/MCP servers.
		if err := w.env.TrustSigner(signer, "trustsurface-publisher@example.com", true); err != nil {
			return fmt.Errorf("trust the trust-surface signer: %w", err)
		}
		return tsWireAndPull(w, url)
	})

	ctx.Step(`^Alice approves the (fragment|skill|MCP server|hook)$`, func(c context.Context, element string) error {
		w := worldFrom(c)
		selector, err := tsSelector(element)
		if err != nil {
			return err
		}
		return runOK(w, "trust", tsRef(w, selector))
	})

	ctx.Step(`^Alice rejects the (fragment|skill|MCP server|hook)$`, func(c context.Context, element string) error {
		w := worldFrom(c)
		selector, err := tsSelector(element)
		if err != nil {
			return err
		}
		return runOK(w, "blacklist", tsRef(w, selector))
	})

	// "Alice starts a session" is steps_j1_setup.go's existing step
	// (materializes "default" into "out") — reused verbatim; godog rejects an
	// ambiguous second match for the same step text (steps_j3.go:229-235).

	ctx.Step(`^the (fragment|skill|MCP server|hook) is present in her assistant's delivered surface$`, func(c context.Context, element string) error {
		return tsAssertPresence(worldFrom(c), element, true)
	})

	ctx.Step(`^the (fragment|skill|MCP server|hook) is absent from her assistant's delivered surface$`, func(c context.Context, element string) error {
		return tsAssertPresence(worldFrom(c), element, false)
	})

	ctx.Step(`^Alice tries to approve the bundle's profile$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("trust", tsRef(w, "profiles/reviewme"))
		return nil // the refusal (non-zero exit + message) is asserted next
	})

	ctx.Step(`^ctxloom refuses, because profiles are not a trust-addressable kind$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = out
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("expected 'ctxloom trust <ref>#profiles/...' to refuse (non-zero exit); got exit 0, output:\n%s", out)
		}
		if !strings.Contains(out, "unknown item kind") {
			return fmt.Errorf("refusal does not name profiles as an unrecognized item kind (want \"unknown item kind\"); output:\n%s", out)
		}
		return nil
	})
}

// tsAssertPresence dispatches to the right generated-surface parser per
// element and asserts the marker's presence/absence in the PAYLOAD — never a
// bare file-exists or exit-code proxy.
func tsAssertPresence(w *World, element string, present bool) error {
	switch element {
	case "fragment":
		return tsAssertFragment(w, present)
	case "skill":
		return tsAssertSkill(w, present)
	case "MCP server":
		return tsAssertMCP(w, present)
	case "hook":
		return tsAssertHook(w, present)
	default:
		return fmt.Errorf("trust-surface: unknown element %q", element)
	}
}

// tsAssertFragment reads the assembled context (out/CLAUDE.md) and asserts
// the fragment's marker landed (or was withheld).
func tsAssertFragment(w *World, present bool) error {
	rel := filepath.Join("out", "CLAUDE.md")
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read materialized %s (materialize output:\n%s): %w", rel, w.env.LastOutput(), err)
	}
	// Excerpt around the marker (reusing steps_j5.go's j5Excerpt) rather than
	// the whole assembled CLAUDE.md, which on this host also carries whatever
	// companions (ltk, taskloom) are on PATH — the excerpt keeps the
	// published page focused on this scenario's own payload. When the marker
	// is genuinely absent there is no line to center on, so state the
	// negative result directly instead of dumping the whole file.
	has := strings.Contains(body, tsFragmentMarker)
	if has {
		w.docStepMaterialized = j5Excerpt(body, tsFragmentMarker, 1)
	} else {
		w.docStepMaterialized = fmt.Sprintf("%s: does not contain %q (%d bytes assembled)", rel, tsFragmentMarker, len(body))
	}
	if present && !has {
		return fmt.Errorf("%s does not contain the fragment's marker %q; content:\n%s", rel, tsFragmentMarker, body)
	}
	if !present && has {
		return fmt.Errorf("%s unexpectedly contains the fragment's marker %q; content:\n%s", rel, tsFragmentMarker, body)
	}
	return nil
}

// tsAssertSkill reads the exported slash-command file (claude flattens
// "<bundle>/<item>" to "<bundle>-<item>.md" — internal/claude/commandfiles.go)
// and asserts the skill's body landed (or the export never happened at all —
// a withheld skill is never written, not written-then-emptied).
func tsAssertSkill(w *World, present bool) error {
	rel := filepath.Join("out", ".claude", "commands", "trustdemo-guide.md")
	body, err := w.env.ReadFile(rel)
	if err != nil {
		if present {
			return fmt.Errorf("read exported skill %s: %w", rel, err)
		}
		w.docStepMaterialized = fmt.Sprintf("%s: not written (skill withheld)", rel)
		return nil
	}
	w.docStepMaterialized = body
	has := strings.Contains(body, tsSkillMarker)
	if present && !has {
		return fmt.Errorf("%s does not contain the skill's marker %q; content:\n%s", rel, tsSkillMarker, body)
	}
	if !present && has {
		return fmt.Errorf("%s unexpectedly contains the skill's marker %q; content:\n%s", rel, tsSkillMarker, body)
	}
	return nil
}

// tsAssertMCP parses the generated .mcp.json (reusing steps_j5.go's
// j5ReadJSON/j5FormatArgs) and asserts the "toolserver" entry's own command
// and args carry the marker — or that the entry is ABSENT entirely, never
// merely emptied.
func tsAssertMCP(w *World, present bool) error {
	rel := filepath.Join("out", ".mcp.json")
	doc, err := j5ReadJSON(w, rel)
	if err != nil {
		return err
	}
	top, _ := doc["mcpServers"].(map[string]any)
	srv, ok := top["toolserver"].(map[string]any)
	if !ok {
		w.docStepMaterialized = fmt.Sprintf("%s → mcpServers: no \"toolserver\" entry", rel)
		if present {
			return fmt.Errorf("%s has no mcpServers.toolserver entry; parsed: %+v", rel, doc)
		}
		return nil
	}
	cmd, _ := srv["command"].(string)
	args := j5FormatArgs(srv["args"])
	w.docStepMaterialized = fmt.Sprintf("%s → mcpServers.toolserver\n  command: %s\n  args:    %s", rel, cmd, args)
	if !present {
		return fmt.Errorf("%s unexpectedly has a mcpServers.toolserver entry (command=%q, args=%s); it should have been withheld", rel, cmd, args)
	}
	if !strings.Contains(args, tsMCPMarker) {
		return fmt.Errorf("%s's toolserver args %q do not contain the marker %q", rel, args, tsMCPMarker)
	}
	return nil
}

// tsAssertHook parses the generated .claude/settings.json (reusing
// steps_j5.go's j5ReadJSON/j5HookCommandsFrom) and asserts the SessionStart
// hook list carries (or lacks) the marker command.
func tsAssertHook(w *World, present bool) error {
	rel := filepath.Join("out", ".claude", "settings.json")
	doc, err := j5ReadJSON(w, rel)
	if err != nil {
		return err
	}
	top, _ := doc["hooks"].(map[string]any)
	cmds := j5HookCommandsFrom(top["SessionStart"])
	found := false
	for _, cmd := range cmds {
		if cmd == tsHookMarker {
			found = true
			break
		}
	}
	w.docStepMaterialized = fmt.Sprintf("%s → hooks.SessionStart\n  commands: %v", rel, cmds)
	if present && !found {
		return fmt.Errorf("%s's SessionStart hooks %v do not include the marker command %q", rel, cmds, tsHookMarker)
	}
	if !present && found {
		return fmt.Errorf("%s's SessionStart hooks %v unexpectedly include the marker command %q", rel, cmds, tsHookMarker)
	}
	return nil
}
