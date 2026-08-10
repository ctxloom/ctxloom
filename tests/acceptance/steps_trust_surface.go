//go:build acceptance

// The trust-surface matrix (trust_surface.feature): a REFERENCE demonstration,
// not a journey — no persona arc, just the exhaustive element × decision table
// this file proves. One bundle ships one of each trust-addressable kind
// (fragment, command, mcp server, hook); each row approves or rejects ONE of
// them and asserts the PAYLOAD — the element's actual bytes in the generated
// surface — landed or did not.
//
// TWO fixtures, matching the feature file's two Outlines, and deliberately
// not one: a denial that starts from a state the item was ALREADY withheld
// from (unsigned, never reviewed) can never fail for the reason it claims to
// prove — rejecting it changes nothing observable, so a broken reject path
// would still pass. Denial is proven from a bundle a TRUSTED publisher
// signed (allowed by default, exactly J001500's Background); approval is proven
// from an unsigned bundle (denied by default). Each Outline's Given step
// seeds the fixture that makes ITS verb meaningful.
//
// Reuses steps_j000200_common.go's ensureProjectWithEngine/runOK, steps_j001500.go's
// signed-remote pattern (testenv.GenerateTestSigner/SeedSignedRemote/
// TrustSigner), and steps_j000400.go's JSON/hook-command parsing helpers
// (j000400ReadJSON, j000400HookCommandsFrom, j000400FormatArgs) rather than re-deriving
// them — same package, same conventions. "Alice starts a session" is
// steps_j000200_setup.go's existing step (materialize "default" into "out"),
// reused VERBATIM.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// Distinctive marker strings this feature's bundle carries, so the generated
// surface can be checked for exactly the right payload — never a bare
// file-exists or exit-code proxy.
const (
	tsFragmentMarker = "TS-FRAGMENT-MARKER-4b7f1a"
	tsCommandMarker  = "TS-COMMAND-MARKER-9d2e60"
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
// steps_j001500.go's j001500State keeps its signer for AdvanceSignedRemote.
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
// command, an MCP server, a hook, and a profile — one of each trust-addressable
// kind, plus the one kind that ISN'T (profiles) — each carrying its own
// distinctive marker. Identical content either signed or not; only the
// SEEDING (signed+trusted vs unsigned) differs between the two fixtures.
func tsBundleYAML() string {
	return fmt.Sprintf(`version: "1.0.0"
fragments:
  context:
    content: %q
commands:
  guide:
    description: trust-surface demo command
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
`, tsFragmentMarker, tsCommandMarker, tsMCPMarker, tsHookMarker)
}

// tsBundleYAMLFragmentRenamed is GAP A's fixture: the SAME bundle
// tsBundleYAML ships, except the fragment's key is "context2" instead of
// "context" — a rename/move at the publisher, with the fragment's BYTES
// (tsFragmentMarker) left byte-for-byte identical. Everything else
// (command/mcp/hook/profile) is unchanged. Used with AdvanceSignedRemote so the
// re-signed document is still validly signed by the same trusted principal —
// if the content-level rejection were not enforced, step 5 (trusted signer)
// would re-admit it under its new name.
func tsBundleYAMLFragmentRenamed() string {
	return fmt.Sprintf(`version: "1.0.0"
fragments:
  context2:
    content: %q
commands:
  guide:
    description: trust-surface demo command
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
`, tsFragmentMarker, tsCommandMarker, tsMCPMarker, tsHookMarker)
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
commands:
  guide:
    description: trust-surface demo command
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
`, tsFragmentMarker, tsFragmentDistilledAddedMarker, tsCommandMarker, tsMCPMarker, tsHookMarker)
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
// steps_j001500.go/steps_content_decision.go drive `ctxloom bundle trust`/`bundle reject` with.
func tsRef(w *World, selector string) string {
	ts := tsOf(w)
	return ts.url + "@bundles/" + ts.bundleName + "#" + selector
}

// tsSelector maps a Scenario Outline's <element> column to the item's
// "<kind>/<name>" selector (internal/operations/trust.go's trust.ParseSelector
// vocabulary: fragments|commands|mcp|hooks).
func tsSelector(element string) (string, error) {
	switch element {
	case "fragment":
		return "fragments/context", nil
	case "command":
		return "commands/guide", nil
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
// journey's sources use (steps_j000200_common.go's addSourceAsRemote, steps_j001500.go's
// j001500WireReference) — then pulls, so the freshly seeded content resolves
// through the config bundle loader before any trust/blacklist/materialize call.
func tsWireAndPull(w *World, url string) error {
	ts := tsOf(w)
	ts.url = url
	if err := runOK(w, "remote", "create", "trustdemo", url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "trustdemo/"+ts.bundleName); err != nil {
		return err
	}
	return runOK(w, "deps", "pull")
}

// tsUpdateAndPull advances an ALREADY-INSTALLED bundle to a newly published
// commit. Plain "deps pull" is passive by design — remote_upgrade.go's own
// doc: "Passive 'deps pull' installs exactly what is already pinned and
// never advances" — so a bundle this feature already pulled once (GAP A's
// rename, GAP B's later-added distilled form) needs 'deps upgrade'
// to actually fetch+apply the new commit before a subsequent pull refreshes
// the lockfile; verified empirically (a plain second pull silently no-ops:
// "Skipped (kept at their locked commit)"). --force skips the interactive per-item
// confirmation prompt (this test drives stdin-less exec.Command).
func tsUpdateAndPull(w *World) error {
	if err := runOK(w, "deps", "upgrade", "--force"); err != nil {
		return err
	}
	return runOK(w, "deps", "pull")
}

func registerTrustSurfaceSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook$`, func(c context.Context) error {
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

	ctx.Step(`^a trusted publisher's signed bundle ships one of each: a fragment, a command, an MCP server, and a hook$`, func(c context.Context) error {
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
		// must beat an allow that is already in effect, exactly as J001500 proves
		// for hooks/MCP servers.
		if err := w.env.TrustSigner(signer, "trustsurface-publisher@example.com", true); err != nil {
			return fmt.Errorf("trust the trust-surface signer: %w", err)
		}
		ts.signer = signer
		return tsWireAndPull(w, url)
	})

	ctx.Step(`^Alice approves the (fragment|command|MCP server|hook)$`, func(c context.Context, element string) error {
		w := worldFrom(c)
		selector, err := tsSelector(element)
		if err != nil {
			return err
		}
		return runOK(w, "bundle", "trust", tsRef(w, selector))
	})

	ctx.Step(`^Alice rejects the (fragment|command|MCP server|hook)$`, func(c context.Context, element string) error {
		w := worldFrom(c)
		selector, err := tsSelector(element)
		if err != nil {
			return err
		}
		return runOK(w, "bundle", "reject", tsRef(w, selector))
	})

	// "Alice starts a session" is steps_j000200_setup.go's existing step
	// (materializes "default" into "out") — reused verbatim; godog rejects an
	// ambiguous second match for the same step text (steps_j001500.go:229-235).

	ctx.Step(`^the (fragment|command|MCP server|hook) is present in her assistant's delivered surface$`, func(c context.Context, element string) error {
		return tsAssertPresence(worldFrom(c), element, true)
	})

	ctx.Step(`^the (fragment|command|MCP server|hook) is absent from her assistant's delivered surface$`, func(c context.Context, element string) error {
		return tsAssertPresence(worldFrom(c), element, false)
	})

	ctx.Step(`^Alice tries to approve the bundle's profile$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("bundle", "trust", tsRef(w, "profiles/reviewme"))
		return nil // the refusal (non-zero exit + message) is asserted next
	})

	ctx.Step(`^ctxloom refuses, because profiles are not a trust-addressable kind$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = out
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("expected 'ctxloom bundle trust <ref>#profiles/...' to refuse (non-zero exit); got exit 0, output:\n%s", out)
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
			w.docStepMaterialized = j000400Excerpt(body, marker, 1)
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
		return runOK(w, "deps", "pull")
	})

	// --- GAP D: a corrupted approvals store denies everything (FIXED) --------

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

	// Fixed behavior is deny-EVERYTHING at its strongest: the corrupt store
	// records a fatal trust finding for the session, so `profile materialize`
	// ABORTS pre-write (strict mode) rather than emitting a surface with the
	// items quietly withheld — nothing is delivered at all, and Alice is told
	// exactly why. "Alice starts a session" runs materialize ignoring its
	// error, so LastExitCode / LastOutput carry the abort.
	ctx.Step(`^her session refuses to start, telling her the approvals store is corrupted$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = tsAbortExcerpt(out)
		if code := w.env.LastExitCode(); code == 0 {
			return fmt.Errorf("materialize exited 0 on a corrupted approvals store; it must refuse to start. output:\n%s", out)
		}
		if !strings.Contains(out, "approvals store unreadable") {
			return fmt.Errorf("materialize output does not tell Alice her approvals store is corrupted; output:\n%s", out)
		}
		return nil
	})

	// The exact refutation of the vulnerability (taskloom rocky-motto): the
	// bug was "reject the fragment -> corrupt the store -> the rejected
	// fragment REAPPEARS in the assembled context, exit 0, no warning". Post
	// -fix, the assembled context (out/CLAUDE.md, rewritten every run) is
	// never regenerated at all — the session aborted — so the rejected
	// fragment's bytes stay gone. A missing/empty CLAUDE.md is the cleanest
	// pass: the refused session assembled nothing, so nothing rejected came
	// back. (The command/MCP/hook were legitimately delivered by the PRIOR,
	// healthy session and its files linger untouched by the aborted run —
	// that stale surface is not the rejected content reappearing, so this
	// step deliberately asserts only on the fragment that was rejected.)
	ctx.Step(`^the previously-rejected fragment has still not reappeared in her assistant's context$`, func(c context.Context) error {
		return tsFragmentNotReappeared(worldFrom(c))
	})

	// --- GAP D2: an unreadable lockfile withholds, rather than un-retracting --

	ctx.Step(`^her lockfile is corrupted, an unparseable lock\.yaml$`, func(c context.Context) error {
		w := worldFrom(c)
		// The pull above wrote a real lock.yaml carrying this bundle's pin
		// (and the field that records a publisher retraction). Replace it with
		// a truncated/half-written file: it EXISTS — so this is genuine
		// corruption, not the legitimate "no lockfile at all" case, which must
		// keep working — and its unterminated quoted scalar fails the parse.
		return w.env.WriteFile(filepath.Join(".ctxloom", "lock.yaml"),
			"version: 1\nbundles:\n  broken:\n    sha: \"abc\n    pinned: true\n")
	})

	// Fail-closed, the same shape as the corrupted approvals store above: the
	// unreadable lockfile records a fatal ClassTrust finding, so `profile
	// materialize` ABORTS pre-write rather than delivering a surface built on
	// retraction state nobody could read. The message discipline is asserted
	// too, because this is a fault a user cannot otherwise diagnose — nothing
	// they typed mentions lock.yaml — so the abort must name the file, say
	// what it withheld and why, and name the recovery.
	ctx.Step(`^her session refuses to start, telling her the retraction state cannot be established, and naming how to recover$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := tsRefuseIfOnlyCompanionsCanTrip(); err != nil {
			return err
		}
		out := w.env.LastOutput()
		w.docStepMaterialized = tsAbortExcerpt(out)
		if code := w.env.LastExitCode(); code == 0 {
			return fmt.Errorf("materialize exited 0 on an unreadable lockfile; it must refuse to start. output:\n%s", out)
		}
		for _, want := range []string{
			"cannot establish retraction state", // the reason
			"lock.yaml",                         // the file
			"withholding remote content",        // what it did, as policy
			"ctxloom remote lock",               // the recovery
			"left intact",                       // the reassurance: nothing was destroyed
		} {
			if !strings.Contains(out, want) {
				return fmt.Errorf("materialize output is missing %q — an abort on a corrupt lockfile a user cannot otherwise diagnose must name the reason, the file, and the recovery; output:\n%s", want, out)
			}
		}
		return nil
	})

	// --- GAP C: the decision that was RECORDED, not just the payload served ----
	//
	// Every scenario above reads the downstream materialized payload. None of
	// them look at what `ctxloom trust`/`blacklist` actually wrote — so a
	// rejection could record a block for a form the item does not have, or
	// silently fail to record one it does, and nothing would notice.
	//
	// These two used to read the CLI's own report (`--format json`'s
	// content_forms), which is the tool's CLAIM about what it wrote, not the
	// write: operations.SetBlacklist appends to that list whenever the store
	// write returns nil, so with countersign.Store's writes neutered to record
	// NOTHING the report still said ["raw"] / ["raw","distilled"] and both rows
	// stayed green (audit irate-catfish, F3) — the exact risk this section's
	// preamble names. They now ask the STORE.

	ctx.Step(`^Alice rejects the fragment, and ctxloom reports what it recorded$`, func(c context.Context) error {
		w := worldFrom(c)
		return runOK(w, "bundle", "reject", tsRef(w, "fragments/context"), "--format", "json")
	})

	ctx.Step(`^the recorded rejection covers exactly the (raw form|raw and distilled forms)$`, func(c context.Context, which string) error {
		return tsAssertRecordedContentRejects(worldFrom(c), which == "raw and distilled forms")
	})

	// --- GAP F: an unparseable source ref must fail CLOSED, never "local" -----

	ctx.Step(`^Alice tries to review an item whose source reference is malformed$`, func(c context.Context) error {
		w := worldFrom(c)
		// "https://" carries a scheme marker (so it was plainly INTENDED as a
		// canonical ref) but does not parse as one. trust.ParseItemRef must
		// refuse it outright rather than silently downgrading it to a local
		// bundle name — a local ref is auto-ALLOWED at cascade step 3, so a
		// downgrade here is a gate bypass, not a cosmetic mislabel.
		_ = w.env.Run("bundle", "trust", "https://#fragments/context")
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

// tsApprovalsStore opens Alice's user countersignature store — where the
// unsigned degraded records (spec §9.5) this key-less environment writes
// actually land.
func tsApprovalsStore(w *World) *countersign.Store {
	dir := filepath.Join(w.env.HomeDir, paths.AppDirName, paths.ApprovalsDirName)
	return countersign.NewStore(dir, afero.NewOsFs())
}

// tsAssertRecordedContentRejects reads GAP C's claim out of the STORE: for each
// attestation form a fragment can be countersigned under, does a content-level
// (ref-omitted) reject marker exist over the bytes that form serves?
//
// It asks countersign.Store's own predicate rather than listing filenames,
// because a record's identity is a hash of its framed preimage: only the
// production lookup can distinguish "the block for THESE bytes in THIS role"
// from "a file of about the right shape". The payloads come from the fixture's
// markers through bundles.BundleFragment.ContentPayload — the same single
// preimage builder the write path used.
//
// BOTH directions are asserted, and the second is the one the section's
// preamble is about: a phantom block for a form the item does not have is not
// harmless bookkeeping. For the raw-only fixture that means the distilled form
// must carry NO block, including none recorded over the raw bytes — which is
// exactly the shape a write path that fell back from distilled to raw would
// leave behind.
func tsAssertRecordedContentRejects(w *World, dualForm bool) error {
	frag := bundles.BundleFragment{Content: tsFragmentMarker}
	if dualForm {
		frag = bundles.BundleFragment{Content: tsDualRawMarker, Distilled: tsDualDistilledMarker}
	}
	rawPayload, _ := frag.ContentPayload(false)
	distilledPayload, _ := frag.ContentPayload(true)

	store := tsApprovalsStore(w)
	var recorded []string
	if store.HasUnsignedContentReject(signing.AttestFragmentRaw, rawPayload) {
		recorded = append(recorded, "raw")
	}
	// For the raw-only fixture ContentPayload(true) falls back to the RAW
	// bytes, so this same call doubles as the phantom check: a distilled-form
	// block over raw bytes is a claim to have blocked a form the item never had.
	if store.HasUnsignedContentReject(signing.AttestFragmentDistilled, distilledPayload) {
		recorded = append(recorded, "distilled")
	}

	want := []string{"raw"}
	if dualForm {
		want = []string{"raw", "distilled"}
	}
	w.docStepMaterialized = fmt.Sprintf("countersign store → content-reject forms actually on record: %v (CLI reported: %s)",
		recorded, tsReportedContentForms(w))
	if len(recorded) != len(want) {
		return fmt.Errorf("the approvals store carries content-reject records for forms %v, want exactly %v.\n"+
			"This reads the STORE, not `bundle reject --format json`'s own report of what it wrote — which said: %s",
			recorded, want, tsReportedContentForms(w))
	}
	for i := range want {
		if recorded[i] != want[i] {
			return fmt.Errorf("the approvals store carries content-reject records for forms %v, want exactly %v", recorded, want)
		}
	}
	return nil
}

// tsReportedContentForms is the CLI's own "content_forms" claim, kept for the
// living-doc evidence pane and for failure messages ONLY — never as the
// assertion. The CLI prints a withheld-advisory warning line around the JSON,
// so the object is located from its first "{" and read with a Decoder, which
// stops at the end of the one value rather than choking on the trailing text.
func tsReportedContentForms(w *World) string {
	out := w.env.LastOutput()
	start := strings.Index(out, "{")
	if start < 0 {
		return "(no JSON object in `bundle reject --format json` output)"
	}
	var res struct {
		Status       string   `json:"status"`
		ContentForms []string `json:"content_forms"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&res); err != nil {
		return fmt.Sprintf("(unparseable: %v)", err)
	}
	return fmt.Sprintf("status=%q content_forms=%v", res.Status, res.ContentForms)
}

// tsSetUseDistilled appends a top-level "config:\n  use_distilled: <bool>\n"
// block to the project's config.yaml (which ensureProjectWithEngine's
// buildJ000200Config never emits one of, so this is additive, never a collision —
// see steps_j000200_common.go's addMockAlongside for the same read-then-append
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

// tsAbortExcerpt pulls the strict-mode abort banner and its first finding out
// of a materialize run's (heavily repeated) warning stream, for the living-doc
// evidence pane — the whole stream is one identical "approvals store
// unreadable" line per resolved item plus the abort listing, so a focused
// excerpt reads far better than dumping all of it.
func tsAbortExcerpt(out string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "aborting startup") ||
			strings.Contains(line, "[trust-store]") ||
			strings.Contains(line, "fix or remove the corrupted approvals store") {
			keep = append(keep, strings.TrimSpace(line))
		}
		if len(keep) >= 3 {
			break
		}
	}
	if len(keep) == 0 {
		return strings.TrimSpace(out)
	}
	return strings.Join(keep, "\n")
}

// tsFragmentNotReappeared asserts the previously-rejected fragment's marker is
// NOT present in the assembled context (out/CLAUDE.md). It is missing-file
// tolerant on purpose: the corrupt-store session ABORTS before regenerating
// CLAUDE.md, so an absent (or present-but-markerless) context both mean the
// same thing — the rejected fragment did not come back. This is the direct
// inverse of the vulnerability, which saw the marker REAPPEAR here.
func tsFragmentNotReappeared(w *World) error {
	rel := filepath.Join("out", "CLAUDE.md")
	body, err := w.env.ReadFile(rel)
	if err != nil {
		// No assembled context at all — the aborted session produced none, so
		// the rejected fragment certainly did not reappear.
		w.docStepMaterialized = fmt.Sprintf("%s: not present — the refused session assembled no context, so the rejected fragment did not reappear", rel)
		return nil
	}
	if strings.Contains(body, tsFragmentMarker) {
		return fmt.Errorf("the rejected fragment's marker %q REAPPEARED in %s after the store was corrupted — the fail-open bug is back; content:\n%s", tsFragmentMarker, rel, body)
	}
	w.docStepMaterialized = fmt.Sprintf("%s: %d bytes assembled, and it does NOT contain the rejected fragment's marker %q — the rejection held through the corruption", rel, len(body), tsFragmentMarker)
	return nil
}

// tsAssertPresence dispatches to the right generated-surface parser per
// element and asserts the marker's presence/absence in the PAYLOAD — never a
// bare file-exists or exit-code proxy.
func tsAssertPresence(w *World, element string, present bool) error {
	switch element {
	case "fragment":
		return tsAssertFragment(w, present)
	case "command":
		return tsAssertCommand(w, present)
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
	// Excerpt around the marker (reusing steps_j000400.go's j000400Excerpt) rather than
	// the whole assembled CLAUDE.md, which on this host also carries whatever
	// companions (ltk, taskloom) are on PATH — the excerpt keeps the
	// published page focused on this scenario's own payload. When the marker
	// is genuinely absent there is no line to center on, so state the
	// negative result directly instead of dumping the whole file.
	has := strings.Contains(body, tsFragmentMarker)
	if has {
		w.docStepMaterialized = j000400Excerpt(body, tsFragmentMarker, 1)
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

// tsAssertCommand reads the exported slash-command file (claude flattens
// "<bundle>/<item>" to "<bundle>-<item>.md" — internal/claude/commandfiles.go)
// and asserts the command's body landed (or the export never happened at all —
// a withheld command is never written, not written-then-emptied).
func tsAssertCommand(w *World, present bool) error {
	rel := filepath.Join("out", ".claude", "commands", "trustdemo-guide.md")
	body, err := w.env.ReadFile(rel)
	if err != nil {
		if present {
			return fmt.Errorf("read exported command %s: %w", rel, err)
		}
		w.docStepMaterialized = fmt.Sprintf("%s: not written (command withheld)", rel)
		return nil
	}
	w.docStepMaterialized = body
	has := strings.Contains(body, tsCommandMarker)
	if present && !has {
		return fmt.Errorf("%s does not contain the command's marker %q; content:\n%s", rel, tsCommandMarker, body)
	}
	if !present && has {
		return fmt.Errorf("%s unexpectedly contains the command's marker %q; content:\n%s", rel, tsCommandMarker, body)
	}
	return nil
}

// tsAssertMCP parses the generated .mcp.json (reusing steps_j000400.go's
// j000400ReadJSON/j000400FormatArgs) and asserts the "toolserver" entry's own command
// and args carry the marker — or that the entry is ABSENT entirely, never
// merely emptied.
func tsAssertMCP(w *World, present bool) error {
	rel := filepath.Join("out", ".mcp.json")
	doc, err := j000400ReadJSON(w, rel)
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
	args := j000400FormatArgs(srv["args"])
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
// steps_j000400.go's j000400ReadJSON/j000400HookCommandsFrom) and asserts the SessionStart
// hook list carries (or lacks) the marker command.
func tsAssertHook(w *World, present bool) error {
	rel := filepath.Join("out", ".claude", "settings.json")
	doc, err := j000400ReadJSON(w, rel)
	if err != nil {
		return err
	}
	top, _ := doc["hooks"].(map[string]any)
	cmds := j000400HookCommandsFrom(top["SessionStart"])
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

// =============================================================================
// THE TEXT→EXEC ESCALATION, AND STALING.
//
// These two scenarios cover the composite attestation form: what a
// countersignature binds names the item's ROLE as well as its bytes, so
// byte-identical items of different kinds can never share one approval — and the
// contract bump that introduced it leaves every earlier approval STALE rather
// than absent.
// =============================================================================

// tsCollisionMCP is the MCP server the collision fixture ships. Its executable
// preimage — the exact bytes a countersignature over it would cover — is what the
// fixture's FRAGMENT carries as its body.
func tsCollisionMCP() bundles.BundleMCP {
	return bundles.BundleMCP{Command: "/bin/echo", Args: []string{tsMCPMarker}}
}

// tsCollisionBundleYAML ships a fragment whose body is byte-for-byte the MCP
// server's executable preimage. No collision search is involved: exec preimages
// are deterministic JSON with every field always emitted, and a fragment's
// payload is its bare body, so a publisher simply pastes one into the other.
//
// The equality is VERIFIED in the step below through the production preimage
// builders rather than asserted here — a fixture that failed to collide would
// make the scenario prove nothing at all.
func tsCollisionBundleYAML(execPayload []byte) string {
	return fmt.Sprintf(`version: "1.0.0"
fragments:
  context:
    content: %q
mcp:
  toolserver:
    command: %q
    args: [%q]
`, string(execPayload), tsCollisionMCP().Command, tsMCPMarker)
}

// tsSupersedeStore rewrites Alice's approvals store into the state a countersign
// contract bump leaves behind: every recorded signature is still on disk and
// still parses, but none of them can be found any more, because the index hash a
// lookup reconstructs is derived from the framed preimage and the framing
// changed. Renaming each record to a hash nothing reconstructs models that
// exactly, without needing an old binary to have written it.
//
// The display-only index.yaml is deliberately left ALONE — it is the only record
// that a human once approved something here, and the whole point of the staling
// requirement is that it survives to say so.
func tsSupersedeStore(w *World) error {
	dir := filepath.Join(w.env.HomeDir, ".ctxloom", "approvals")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read approvals store %s: %w", dir, err)
	}
	moved := 0
	for _, e := range entries {
		name := e.Name()
		// Both record shapes: a signed .sig and the degraded unsigned marker
		// (spec §9.5) this key-less environment actually writes. Each is named
		// for the index hash of its framed preimage, so both go stale together.
		if e.IsDir() || (!strings.HasSuffix(name, ".sig") && !strings.HasSuffix(name, ".unsigned")) {
			continue
		}
		// The filename's leading field is the index hash; perturbing it is what
		// makes the record unreachable, exactly as a reframed preimage would.
		superseded := "0000" + name[4:]
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, superseded)); err != nil {
			return fmt.Errorf("supersede approval record %s: %w", name, err)
		}
		moved++
	}
	if moved == 0 {
		return fmt.Errorf("no approval records found in %s — the approve step must have written one, or this scenario proves nothing", dir)
	}
	index, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
	if err != nil {
		return fmt.Errorf("the display index must survive a contract bump (it is what reports staleness): %w", err)
	}
	w.docStepMaterialized = fmt.Sprintf("%d approval record(s) superseded; the display index survives:\n%s", moved, strings.TrimSpace(string(index)))
	return nil
}

// tsCountersignRef is operations.countersignRef, reached through the same
// PRODUCTION parse the CLI performs on its own argument: trust.ParseItemRef
// turns the "<url>@bundles/<name>#<kind>/<item>" string a scenario passes to
// `ctxloom bundle trust` into a trust.Ref, whose CanonicalURL and Key compose
// the ref a countersignature actually binds to.
func tsCountersignRef(cliRef string) (string, error) {
	parsed, _, _, err := trust.ParseItemRef(cliRef)
	if err != nil {
		return "", fmt.Errorf("parse item ref %q: %w", cliRef, err)
	}
	return parsed.CanonicalURL() + "|" + parsed.Key(), nil
}

// tsAssertCollisionRoles is the assertion the text→exec escalation scenario
// turns on, and the only one on this page that can fail when the composite
// attestation form is deleted.
//
// The scenario's delivered-surface half CANNOT fail: an approval is keyed by
// ref AND form, trust.Ref.Key bakes the item KIND into the ref, and the fixture's
// two items are #fragments/context and #mcp/toolserver — so the ref component
// alone already separates them and the role plays no part (audit
// irate-catfish, F4). This reads the ROLE out of the store instead, over the
// one payload both items share.
//
// Before the executable has been approved (mcpApproved=false) the text's
// approval must exist under fragment/raw and NOTHING may cover those bytes as
// exec/mcp. After it has, BOTH roles must be on record, each under its own ref
// — and neither may have been written under the other's form, which is exactly
// what a collapsed form mapping would produce.
func tsAssertCollisionRoles(w *World, mcpApproved bool) error {
	mcp := tsCollisionMCP()
	payload, err := mcp.ContentPayload()
	if err != nil {
		return fmt.Errorf("build the mcp executable preimage: %w", err)
	}
	fragRef, err := tsCountersignRef(tsRef(w, "fragments/context"))
	if err != nil {
		return err
	}
	mcpRef, err := tsCountersignRef(tsRef(w, "mcp/toolserver"))
	if err != nil {
		return err
	}
	store := tsApprovalsStore(w)

	// The text's decision, in the text's role. True in both phases: approving
	// the executable must not disturb or absorb it.
	if !store.HasUnsignedApprove(fragRef, signing.AttestFragmentRaw, payload) {
		return fmt.Errorf("no approval of %s is on record under %q over the %d shared bytes — Alice approved the fragment, "+
			"so this is the decision she actually made", fragRef, signing.AttestFragmentRaw, len(payload))
	}
	// A decision may never be recorded under the OTHER item's role. Under a
	// collapsed mapping (exec/mcp folded onto fragment/raw) the executable's
	// approval lands here.
	if store.HasUnsignedApprove(mcpRef, signing.AttestFragmentRaw, payload) {
		return fmt.Errorf("an approval of the EXECUTABLE %s is on record under the TEXT role %q: the two roles share a key, "+
			"so approving a fragment and approving the executable whose bytes it copies are the same decision",
			mcpRef, signing.AttestFragmentRaw)
	}
	if store.HasUnsignedApprove(fragRef, signing.AttestExecMCP, payload) {
		return fmt.Errorf("an approval of the TEXT %s is on record under the EXECUTABLE role %q — approving a fragment "+
			"must never attest its bytes as something that runs", fragRef, signing.AttestExecMCP)
	}

	execOnRecord := store.HasUnsignedApprove(mcpRef, signing.AttestExecMCP, payload)
	if mcpApproved && !execOnRecord {
		return fmt.Errorf("Alice approved the MCP server, but no approval of %s is on record under %q over those same %d bytes; "+
			"the executable's decision has to be recorded IN THE EXECUTABLE'S ROLE, or it is indistinguishable from the "+
			"text's", mcpRef, signing.AttestExecMCP, len(payload))
	}
	if !mcpApproved && execOnRecord {
		return fmt.Errorf("nobody has approved the executable yet, but %s is already on record under %q — an approval of "+
			"the text expanded to cover the thing that runs", mcpRef, signing.AttestExecMCP)
	}
	w.docStepMaterialized = fmt.Sprintf("countersign store over the %d shared bytes:\n  %s → %s: on record\n  %s → %s: %v",
		len(payload), fragRef, signing.AttestFragmentRaw, mcpRef, signing.AttestExecMCP, execOnRecord)
	return nil
}

func registerTrustVocabularySteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a bundle from an unsigned, never-reviewed publisher ships a fragment whose body is byte-identical to its MCP server's executable preimage$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		mcp := tsCollisionMCP()
		execPayload, err := mcp.ContentPayload()
		if err != nil {
			return fmt.Errorf("build the mcp executable preimage: %w", err)
		}
		// The collision, verified through the two PRODUCTION preimage builders:
		// the fragment the reviewer will be shown as text and the executable the
		// gate will ask about resolve to identical bytes.
		frag := bundles.BundleFragment{Content: string(execPayload)}
		fragPayload, _ := frag.ContentPayload(false)
		if !bytes.Equal(fragPayload, execPayload) {
			return fmt.Errorf("fixture does not actually collide: fragment payload %q != mcp preimage %q", fragPayload, execPayload)
		}
		ts := tsOf(w)
		rel := ".ctxloom/content/bundles/" + ts.bundleName + ".yaml"
		url, err := w.env.SeedRemote(map[string]string{rel: tsCollisionBundleYAML(execPayload)})
		if err != nil {
			return fmt.Errorf("seed collision trust-surface remote: %w", err)
		}
		return tsWireAndPull(w, url)
	})

	ctx.Step(`^the copied preimage is present in her assistant's delivered surface as text$`, func(c context.Context) error {
		w := worldFrom(c)
		rel := filepath.Join("out", "CLAUDE.md")
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read materialized %s (materialize output:\n%s): %w", rel, w.env.LastOutput(), err)
		}
		if !strings.Contains(body, tsMCPMarker) {
			return fmt.Errorf("%s does not carry the approved fragment's body; the approval Alice actually made must be honoured, or this scenario proves nothing:\n%s", rel, body)
		}
		w.docStepMaterialized = j000400Excerpt(body, tsMCPMarker, 1)
		return nil
	})

	ctx.Step(`^what Alice approved is on record as the text's role only, never as the executable's$`, func(c context.Context) error {
		return tsAssertCollisionRoles(worldFrom(c), false)
	})

	ctx.Step(`^the two decisions are on record as separate attestations over the same bytes, one per role$`, func(c context.Context) error {
		return tsAssertCollisionRoles(worldFrom(c), true)
	})

	ctx.Step(`^her approval was recorded under a superseded countersign contract$`, func(c context.Context) error {
		return tsSupersedeStore(worldFrom(c))
	})

	ctx.Step(`^review lists the fragment as an update awaiting re-review, not as a new item$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runOK(w, "review", "--list"); err != nil {
			return err
		}
		out := w.env.LastOutput()
		w.docStepMaterialized = strings.TrimSpace(out)
		if !strings.Contains(out, "update") {
			return fmt.Errorf("`review --list` does not label the superseded item an update — a stale approval must not read as a first-time item; output:\n%s", out)
		}
		if !strings.Contains(out, "fragments/context") {
			return fmt.Errorf("`review --list` does not list the superseded fragment at all; output:\n%s", out)
		}
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, "fragments/context") {
				continue
			}
			if !strings.Contains(line, "update") {
				return fmt.Errorf("the superseded fragment is listed as %q, want it labelled an update; output:\n%s", strings.TrimSpace(line), out)
			}
			return nil
		}
		return fmt.Errorf("`review --list` listed no line for fragments/context; output:\n%s", out)
	})
}

// tsRefuseIfOnlyCompanionsCanTrip is the fail-loud guard for the one scenario
// in this file that is green for the wrong reason (taskloom alive-rover).
//
// MEASURED here, by instrumenting operations.EffectiveTrust's
// retraction-readable arm and running the scenario alone: with the lockfile
// corrupted, the refs that reach that arm are ltk's and taskloom's — seven
// COMPANION items — and the remote `trustdemo` bundle this scenario is named
// after reaches it ZERO times. The mechanism is remote.BundleReader: it serves
// a remote bundle's bytes only at the SHA its lockfile entry records, so an
// unparseable lock.yaml means ErrBundleNotInLockfile and the bundle is never
// loaded, hence never trust-evaluated, hence produces no finding. The abort
// the assertions below read is therefore raised entirely by companion content
// tripping the same gate.
//
// That makes the outcome depend on whether the machine has ltk/taskloom on
// PATH rather than on the behaviour under test: someone could delete the
// retraction-readability check for remote content and this scenario would
// stay green wherever companions are installed. The fixture cannot fix it —
// there is no way to keep a remote bundle loadable through a lockfile that
// does not parse — so the honest interim is to fail LOUDLY, naming the
// reason, on any machine where the accident that makes it pass is absent,
// rather than failing with a message about retraction state that would send
// the reader hunting in the wrong place.
func tsRefuseIfOnlyCompanionsCanTrip() error {
	for _, bin := range []string{"ltk", "taskloom"} {
		if _, err := exec.LookPath(bin); err == nil {
			return nil
		}
	}
	return fmt.Errorf("this scenario cannot certify what it claims on this machine: " +
		"the retraction-readability gate is reached ONLY by companion content (ltk/taskloom), " +
		"because a remote bundle is served at its locked SHA and an unparseable lock.yaml " +
		"means it is never loaded at all — so with no companion on PATH nothing trips the gate " +
		"and the session does not abort. This is not a regression in the code under test; " +
		"it is the fixture gap filed as taskloom alive-rover, which needs the REMOTE bundle " +
		"to produce the finding")
}
