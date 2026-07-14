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
	"encoding/json"
	"fmt"
	"os"
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

	// tsFragmentDistilledAddedMarker is GAP-B's "form flip" fixture: the bytes
	// a distilled form ADDED AFTER approval carries. Distinct from
	// tsFragmentMarker (the raw form, approved) so presence of EITHER one is
	// individually distinguishable in the assembled context.
	tsFragmentDistilledAddedMarker = "TS-FRAGMENT-DISTILLED-ADDED-MARKER-8e21f0"

	// tsDualRawMarker/tsDualDistilledMarker are GAP-B's "both forms shipped at
	// once" fixture: a fragment carrying distinct raw and distilled content
	// from the start, so approving it and then flipping use_distilled proves
	// an approval covers BOTH forms it saw, not just whichever one a caller
	// happened to check first.
	tsDualRawMarker       = "TS-DUALFORM-RAW-MARKER-2f9c81"
	tsDualDistilledMarker = "TS-DUALFORM-DISTILLED-MARKER-71ac9d"
)

// tsState is this feature's fixture state: the seeded remote's URL, the
// bundle name inside it, and (signed-fixture only) the trusted principal and
// its signer — retained so a later step can RE-SIGN the bundle after
// legitimately changing it (GAP A's rename-and-resign), the same way
// steps_j3.go's j3State keeps its signer for AdvanceSignedRemote.
type tsState struct {
	url        string
	bundleName string
	signer     *testenv.TestSigner
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

// tsBundleYAMLFragmentRenamed is GAP A's fixture: the SAME bundle
// tsBundleYAML ships, except the fragment's key is "context2" instead of
// "context" — a rename/move at the publisher, with the fragment's BYTES
// (tsFragmentMarker) left byte-for-byte identical. Everything else
// (skill/mcp/hook/profile) is unchanged. Used with AdvanceSignedRemote so the
// re-signed document is still validly signed by the same trusted principal —
// if the content-level rejection were not enforced, step 5 (trusted signer)
// would re-admit it under its new name.
func tsBundleYAMLFragmentRenamed() string {
	return fmt.Sprintf(`version: "1.0.0"
fragments:
  context2:
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

// tsBundleYAMLFragmentDistilledAdded is GAP B's "form flip" fixture: the same
// bundle, except the fragment now ALSO carries a "distilled:" field
// (tsFragmentDistilledAddedMarker) the publisher added after Alice already
// approved the raw-only version. The raw bytes (tsFragmentMarker) are left
// unchanged — only a new form is added.
func tsBundleYAMLFragmentDistilledAdded() string {
	return fmt.Sprintf(`version: "1.0.0"
fragments:
  context:
    content: %q
    distilled: %q
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
`, tsFragmentMarker, tsFragmentDistilledAddedMarker, tsSkillMarker, tsMCPMarker, tsHookMarker)
}

// tsDualFormBundleYAML is GAP B's "both forms shipped at once" fixture: a
// single fragment named "context" (the same selector tsSelector("fragment")
// resolves) carrying DISTINCT raw and distilled content from the start, so
// approving it exercises SetItemTrust's dual-form write path (both
// writeApprove calls, not just the raw one every other fixture in this file
// exercises).
func tsDualFormBundleYAML() string {
	return fmt.Sprintf(`version: "1.0.0"
fragments:
  context:
    content: %q
    distilled: %q
`, tsDualRawMarker, tsDualDistilledMarker)
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

// tsUpdateAndPull advances an ALREADY-INSTALLED bundle to a newly published
// commit. Plain "remote pull" is passive by design — remote_upgrade.go's own
// doc: "Passive 'remote pull' installs exactly what is already pinned and
// never advances" — so a bundle this feature already pulled once (GAP A's
// rename, GAP B's later-added distilled form) needs 'remote update --apply'
// to actually fetch+apply the new commit before a subsequent pull refreshes
// the lockfile; verified empirically (a plain second pull silently no-ops:
// "Skipped (already installed)"). --force skips the interactive per-item
// confirmation prompt (this test drives stdin-less exec.Command).
func tsUpdateAndPull(w *World) error {
	if err := runOK(w, "remote", "update", "--apply", "--force"); err != nil {
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
		ts.signer = signer
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

	// --- GAP A: content-hash (byte-level) rejection survives a rename/move ----

	ctx.Step(`^the publisher renames the fragment to a new name, keeping its bytes identical, and re-signs it$`, func(c context.Context) error {
		w := worldFrom(c)
		ts := tsOf(w)
		if ts.signer == nil {
			return fmt.Errorf("trust-surface: rename-and-resign requires the signed fixture (no signer recorded)")
		}
		bareDir := strings.TrimPrefix(ts.url, "file://")
		rel := ".ctxloom/content/bundles/" + ts.bundleName + ".yaml"
		if err := w.env.AdvanceSignedRemote(bareDir, map[string]string{rel: tsBundleYAMLFragmentRenamed()}, []string{rel}, ts.signer); err != nil {
			return fmt.Errorf("advance signed trust-surface remote (rename fragment): %w", err)
		}
		return tsUpdateAndPull(w)
	})

	// --- GAP B: dual-form (raw + distilled) approve/reject ---------------------

	ctx.Step(`^a bundle from an unsigned, never-reviewed publisher ships a fragment with both a raw and a distilled form$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		ts := tsOf(w)
		rel := ".ctxloom/content/bundles/" + ts.bundleName + ".yaml"
		url, err := w.env.SeedRemote(map[string]string{rel: tsDualFormBundleYAML()})
		if err != nil {
			return fmt.Errorf("seed dual-form trust-surface remote: %w", err)
		}
		return tsWireAndPull(w, url)
	})

	ctx.Step(`^Alice starts a session preferring (raw|distilled) content$`, func(c context.Context, form string) error {
		w := worldFrom(c)
		if err := tsSetUseDistilled(w, form == "distilled"); err != nil {
			return err
		}
		_ = w.env.Run("profile", "materialize", "default", "--target", "out")
		return nil
	})

	ctx.Step(`^the fragment's (raw|distilled) marker is present in her assistant's delivered surface$`, func(c context.Context, form string) error {
		w := worldFrom(c)
		rel := filepath.Join("out", "CLAUDE.md")
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read materialized %s (materialize output:\n%s): %w", rel, w.env.LastOutput(), err)
		}
		marker, other := tsDualRawMarker, tsDualDistilledMarker
		if form == "distilled" {
			marker, other = tsDualDistilledMarker, tsDualRawMarker
		}
		has := strings.Contains(body, marker)
		if has {
			w.docStepMaterialized = j5Excerpt(body, marker, 1)
		} else {
			w.docStepMaterialized = fmt.Sprintf("%s: does not contain %q (%d bytes assembled)", rel, marker, len(body))
		}
		if !has {
			return fmt.Errorf("%s does not contain the %s form's marker %q; content:\n%s", rel, form, marker, body)
		}
		// The approval covers BOTH forms, but the CONFIG PREFERENCE must still
		// pick exactly one at a time — the other form's marker appearing too
		// would mean the preference did nothing, not that both are approved.
		if strings.Contains(body, other) {
			return fmt.Errorf("%s unexpectedly ALSO contains the other form's marker %q while preferring %s; content:\n%s", rel, other, form, body)
		}
		return nil
	})

	ctx.Step(`^the publisher adds a distilled form to the fragment, keeping its raw bytes unchanged$`, func(c context.Context) error {
		w := worldFrom(c)
		ts := tsOf(w)
		bareDir := strings.TrimPrefix(ts.url, "file://")
		rel := ".ctxloom/content/bundles/" + ts.bundleName + ".yaml"
		if err := w.env.AdvanceRemote(bareDir, map[string]string{rel: tsBundleYAMLFragmentDistilledAdded()}); err != nil {
			return fmt.Errorf("advance unsigned trust-surface remote (add distilled form): %w", err)
		}
		return tsUpdateAndPull(w)
	})

	ctx.Step(`^the fragment is withheld entirely, in neither its raw nor its new distilled form$`, func(c context.Context) error {
		w := worldFrom(c)
		rel := filepath.Join("out", "CLAUDE.md")
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read materialized %s (materialize output:\n%s): %w", rel, w.env.LastOutput(), err)
		}
		hasRaw := strings.Contains(body, tsFragmentMarker)
		hasDistilled := strings.Contains(body, tsFragmentDistilledAddedMarker)
		w.docStepMaterialized = fmt.Sprintf("%s: raw marker present=%v, new distilled marker present=%v (%d bytes assembled)", rel, hasRaw, hasDistilled, len(body))
		if hasRaw || hasDistilled {
			return fmt.Errorf("%s unexpectedly exposes the fragment (raw=%v, distilled=%v) after an unapproved distilled form was added; content:\n%s", rel, hasRaw, hasDistilled, body)
		}
		return nil
	})

	// --- GAP E: the review STATE LABEL, not just the payload -------------------

	ctx.Step(`^the fragment's review state is "(pending|accepted|rejected)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		state, err := tsFragmentListState(w, "context")
		if err != nil {
			return err
		}
		if state != want {
			return fmt.Errorf("fragment's review state is %q, want %q", state, want)
		}
		return nil
	})

	ctx.Step(`^the publisher retracts the bundle$`, func(c context.Context) error {
		w := worldFrom(c)
		ts := tsOf(w)
		bareDir := strings.TrimPrefix(ts.url, "file://")
		manifest := fmt.Sprintf(
			"version: 1\nretracted:\n  - type: bundle\n    name: %q\n    version: \"\"\n    reason: %q\n",
			ts.bundleName, "trust-surface GAP-E retraction demo")
		if err := w.env.AdvanceRemote(bareDir, map[string]string{".ctxloom/content/manifest.yaml": manifest}); err != nil {
			return fmt.Errorf("advance trust-surface remote with a retraction manifest: %w", err)
		}
		return runOK(w, "remote", "pull")
	})

	// --- GAP D: a corrupted approvals store — see doc.md, @wip in the .feature -

	ctx.Step(`^her approvals store is corrupted, a file where a directory should be$`, func(c context.Context) error {
		w := worldFrom(c)
		dir := filepath.Join(w.env.HomeDir, ".ctxloom", "approvals")
		// The user store (not project) is what "Alice rejects the X" wrote to
		// (SetBlacklist's default is the personal store) — remove the directory
		// entirely and replace it with a plain FILE, so countersign.Store.
		// Readable's afero.ReadDir call fails with a non-ENOENT error (a real
		// I/O failure), not "does not exist yet" (the ordinary, harmless shape).
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove approvals dir to corrupt it: %w", err)
		}
		return w.env.WriteHomeFile(".ctxloom/approvals", "not a directory\n")
	})

	ctx.Step(`^the fragment is present in her assistant's delivered surface, a confirmed product gap$`, func(c context.Context) error {
		return tsAssertPresence(worldFrom(c), "fragment", true)
	})

	// --- GAP C: the decision that was RECORDED, not just the payload served ----
	//
	// Every scenario above reads the downstream materialized payload. None of
	// them look at what `ctxloom trust`/`blacklist` actually wrote — so a
	// rejection could record a block for a form the item does not have, or
	// silently fail to record one it does, and nothing would notice. These two
	// read the decision back out of the CLI's own report of it.

	ctx.Step(`^Alice rejects the fragment, and ctxloom reports what it recorded$`, func(c context.Context) error {
		w := worldFrom(c)
		return runOK(w, "blacklist", tsRef(w, "fragments/context"), "--format", "json")
	})

	ctx.Step(`^the recorded rejection covers exactly the (raw form|raw and distilled forms)$`, func(c context.Context, which string) error {
		w := worldFrom(c)
		want := []string{"raw"}
		if which == "raw and distilled forms" {
			want = []string{"raw", "distilled"}
		}
		got, err := tsRejectedContentForms(w)
		if err != nil {
			return err
		}
		if len(got) != len(want) {
			return fmt.Errorf("rejection recorded content blocks for forms %v, want exactly %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				return fmt.Errorf("rejection recorded content blocks for forms %v, want exactly %v", got, want)
			}
		}
		return nil
	})

	// --- GAP F: an unparseable source ref must fail CLOSED, never "local" -----

	ctx.Step(`^Alice tries to review an item whose source reference is malformed$`, func(c context.Context) error {
		w := worldFrom(c)
		// "https://" carries a scheme marker (so it was plainly INTENDED as a
		// canonical ref) but does not parse as one. parseTrustItemRef must
		// refuse it outright rather than silently downgrading it to a local
		// bundle name — a local ref is auto-ALLOWED at cascade step 3, so a
		// downgrade here is a gate bypass, not a cosmetic mislabel.
		_ = w.env.Run("trust", "https://#fragments/context")
		return nil // the refusal is asserted next
	})

	ctx.Step(`^ctxloom refuses, rather than treating an unrecognized source as local$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = strings.TrimSpace(out)
		if !strings.Contains(out, "refusing to treat an unrecognized source as local") {
			return fmt.Errorf("ctxloom did not refuse the malformed source ref for the stated fail-closed reason "+
				"(want %q); output:\n%s", "refusing to treat an unrecognized source as local", out)
		}
		return nil
	})
}

// tsRejectedContentForms parses the JSON `ctxloom blacklist --format json`
// emitted (operations.SetBlacklistResult) and returns its "content_forms" —
// the forms a CONTENT-level (ref-omitted) block was actually written for. This
// is the recorded DECISION, read back from the tool's own report of it, as
// distinct from the downstream payload every other assertion on this page
// checks. The CLI prints a withheld-advisory warning line before the JSON, so
// the object is located from its first "{" rather than parsing the whole
// stream.
func tsRejectedContentForms(w *World) ([]string, error) {
	out := w.env.LastOutput()
	start := strings.Index(out, "{")
	if start < 0 {
		return nil, fmt.Errorf("`blacklist --format json` emitted no JSON object; output:\n%s", out)
	}
	var res struct {
		Status       string   `json:"status"`
		ContentForms []string `json:"content_forms"`
	}
	// A Decoder (not Unmarshal) because the CLI also prints a withheld-advisory
	// warning line AFTER the JSON object — Unmarshal rejects the trailing text,
	// while a Decoder reads exactly the one value the command emitted.
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&res); err != nil {
		return nil, fmt.Errorf("parse `blacklist --format json` output: %w\noutput:\n%s", err, out)
	}
	w.docStepMaterialized = fmt.Sprintf("blacklist --format json → status=%q content_forms=%v", res.Status, res.ContentForms)
	return res.ContentForms, nil
}

// tsSetUseDistilled appends a top-level "config:\n  use_distilled: <bool>\n"
// block to the project's config.yaml (which ensureProjectWithEngine's
// buildJ1Config never emits one of, so this is additive, never a collision —
// see steps_j1_common.go's addMockAlongside for the same read-then-append
// convention). internal/config has no CLI setter for this value; a direct
// config.yaml edit is the only way a black-box CLI-driving test can toggle it.
func tsSetUseDistilled(w *World, use bool) error {
	body, err := w.env.ReadFile(".ctxloom/config.yaml")
	if err != nil {
		return fmt.Errorf("read config.yaml: %w", err)
	}
	body += fmt.Sprintf("config:\n  use_distilled: %t\n", use)
	return w.env.WriteFile(".ctxloom/config.yaml", body)
}

// tsFragmentListState runs `ctxloom fragment list --format json` and returns
// the named fragment's "state" field (internal/cli/item_helpers.go's itemRow,
// stamped by operations.NewTrustStamper — the SAME TrustStamper/EffectiveTrust
// path materialize uses) — the review-state LABEL a human sees in `ctxloom
// review`/list JSON, as distinct from whether the payload happens to be
// present (see trust_surface.doc.md's GAP-E note: both SourceRejected and
// SourceRetracted must independently render this as "rejected", never
// "pending").
func tsFragmentListState(w *World, name string) (string, error) {
	if err := runOK(w, "fragment", "list", "--format", "json"); err != nil {
		return "", err
	}
	out := w.env.LastOutput()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return "", fmt.Errorf("parse `fragment list --format json` output: %w\noutput:\n%s", err, out)
	}
	for _, row := range rows {
		if n, _ := row["name"].(string); n == name {
			state, _ := row["state"].(string)
			w.docStepMaterialized = fmt.Sprintf("fragment list --format json → %q: state=%q trust_source=%v trusted=%v",
				name, state, row["trust_source"], row["trusted"])
			return state, nil
		}
	}
	return "", fmt.Errorf("fragment %q not found in `fragment list --format json` output:\n%s", name, out)
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
