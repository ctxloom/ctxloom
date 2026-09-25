//go:build acceptance

// J001500: the "content my company has validated" journey (j001500_corporate_signed.feature)
// — signing/trust as the product's value proposition. Cast: Trent (the
// company/trusted publisher), Alice (the developer), Mallory (the attacker who
// wants to change what reaches Alice's assistant, not read it).
//
// Reuses J000200's signing test helpers (tests/integration/testenv/signing_acceptance.go:
// TestSigner, SeedSignedTreeRemote, TrustSigner, AdvanceSignedTreeRemote) and J000200's
// scaffolding helpers (steps_j000200_common.go: ensureProjectWithEngine, runOK) rather
// than rebuilding trust infrastructure that already exists. World carries exactly
// one new field (j001500 *j001500State) to hold this journey's fixture state.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/core/bundles"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// Distinctive marker strings this journey's bundles carry, so a materialized
// context / generated settings file can be checked for exactly the right
// payload (ASSERTION DISCIPLINE) rather than a bare exit-code or file-exists
// proxy.
const (
	j001500CompanyMarker  = "J001500-COMPANY-SECURECODING-MARKER"
	j001500TamperedMarker = "J001500-TAMPERED-CONTENT-MARKER"
	j001500ExtraMarkerA   = "J001500-COMPANY-EXTRA-A-MARKER"
	j001500ExtraMarkerB   = "J001500-COMPANY-EXTRA-B-MARKER"
	j001500HookMarker     = "echo J001500-HOOK-EXEC-MARKER"
	j001500MCPMarker      = "J001500-MCP-EXEC-MARKER"
	j001500ForgeryMarker  = "J001500-FORGERY-BYSTANDER-MARKER"
	j001500UnsignedMarker = "J001500-UNSIGNED-OUTSIDE-MARKER"
)

// The unsigned bundle Alice references from outside the company, by remote and
// bundle name.
const (
	j001500UnsignedRemote = "outside"
	j001500UnsignedBundle = "outside-tools"
)

// j001500WithheldLine captures one per-item withheld advisory line
// (operations.WarnWithheldBy): the ref, possibly empty, and its reason. The
// reason stops at a double quote because off a terminal each warning is a JSON
// object, whose closing `"}` would otherwise read as part of the reason.
var j001500WithheldLine = regexp.MustCompile(`(?m)withheld (\S*): ([^"\n]*)`)

// j001500CheckHeldReason checks one held item's reason against bare, the
// reason's own rendering with no detail: a fragment's is exactly that, and an
// executable's extends it with what would admit it.
func j001500CheckHeldReason(ref, reason, bare string) error {
	if strings.Contains(ref, "#fragment") {
		if reason != bare {
			return fmt.Errorf("the held guidance's reason is %q, want exactly %q: a fragment has nothing to add", reason, bare)
		}
		return nil
	}
	if !strings.HasPrefix(reason, bare) || reason == bare {
		return fmt.Errorf("the held executable %s says only %q; it must also say what would admit it", ref, reason)
	}
	return nil
}

// j001500State is this journey's fixture state: the company's signed bundle
// (signer identity, seeded remote, bundle name), whether Alice has wired it
// into her project yet, and bookkeeping the later scenarios need (rejected
// hook, extra company-signed bundles, the review --project PTY session's
// outcome).
type j001500State struct {
	signer     *testenv.TestSigner
	principal  string
	url        string // file://<bare> — the seeded remote's clone URL
	bare       string // bare repo path (no file:// prefix), for AdvanceRemote/AdvanceSignedTreeRemote
	bundleName string
	referenced bool // remote added + profile modified (addRemoteBundleBase wiring done)

	extraMarkers []string // scenario 6: additional company-signed bundles' markers

	reviewPTYOutput string // scenario 7: captured `ctxloom review --project` pty output
	reviewPTYExit   int

	tamperPullOutput string // scenario 2: the refused `deps pull`'s output
	tamperPullExit   int
}

// j001500Of returns (lazily creating) this scenario's J001500 fixture state.
func j001500Of(w *World) *j001500State {
	if w.j001500 == nil {
		w.j001500 = &j001500State{bundleName: "secure-coding"}
	}
	return w.j001500
}

// j001500TreeEnvelope is every j001500 fixture's tree envelope; the items are
// files beside it.
const j001500TreeEnvelope = "version: \"1.0.0\"\n"

// j001500TreeItems builds the item-file map for a j001500 bundle: one
// fragment named "guidance" whose content IS marker — the payload this
// journey's content scenarios assert reached (or was withheld from) the
// assembled context — and, when withExec is true, an MCP server and a
// session-start hook, each carrying its own distinctive marker in its
// command, for scenarios 3/4's executable-trust-gate assertions against the
// GENERATED settings files (.mcp.json / .claude/settings.json).
//
// The fragment body carries no trailing newline: j001500's content
// assertions are substring checks (tolerant either way), but staying
// consistent with steps_trust_surface.go's tsFullTreeItems — which learned
// the hard way that an exact content-hash lookup is not — costs nothing here.
func j001500TreeItems(marker string, withExec bool) map[string]string {
	items := map[string]string{"fragments/guidance.md": marker}
	if withExec {
		items["mcp/demo-server.yaml"] = fmt.Sprintf("command: %q\nargs: [%q]\n", "/bin/echo", j001500MCPMarker)
		items["hooks/session_start/guard.yaml"] = fmt.Sprintf("type: command\ncommand: %q\n", j001500HookMarker)
	}
	return items
}

// j001500WireReference adds the company's remote and references its bundle from
// the "default" profile — WITHOUT pulling. Idempotent: a second call is a
// no-op. Split from j001500EnsureReferenced so the tamper scenario (which has no
// antecedent "Alice references..." step) can wire the reference before
// Mallory's tampered commit exists, and only pull afterward.
func j001500WireReference(w *World) error {
	j001500 := j001500Of(w)
	if j001500.referenced {
		return nil
	}
	if j001500.url == "" {
		return fmt.Errorf("the company's bundle was never seeded")
	}
	if err := runOK(w, "remote", "create", "company", j001500.url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "company/"+j001500.bundleName); err != nil {
		return err
	}
	j001500.referenced = true
	return nil
}

// j001500EnsureReferenced wires the reference if needed, then ALWAYS pulls — the
// only way to fetch whatever is currently at the remote's HEAD (including a
// commit added after the reference was first wired, e.g. Mallory's tamper or
// the company shipping an MCP server/hook onto an already-referenced bundle).
func j001500EnsureReferenced(w *World) error {
	if err := j001500WireReference(w); err != nil {
		return err
	}
	return runOK(w, "deps", "pull")
}

// j001500StartSession is "Alice starts a session": ensure the company bundle is
// referenced and pulled, then materialize the default profile into "out" —
// the single command that writes CLAUDE.md AND the generated settings files
// (.mcp.json, .claude/settings.json) together (operations.MaterializeProfile),
// so scenarios 1/3/4 share one implementation. Exit code is ignored here (a
// strictness abort on a fatal trust finding is itself asserted by dedicated
// Then steps, mirroring steps_j000200_setup.go's materializeDefault).
func j001500StartSession(w *World) error {
	if err := j001500EnsureReferenced(w); err != nil {
		return err
	}
	_ = w.env.Run("profile", "materialize", "default", "--target", "out")
	return nil
}

// j001500ReadMaterialized reads out/CLAUDE.md, the assembled content surface.
func j001500ReadMaterialized(w *World) (string, error) {
	body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
	if err != nil {
		return "", fmt.Errorf("read materialized out/CLAUDE.md (materialize output:\n%s): %w", w.env.LastOutput(), err)
	}
	// Surface the assembled context to the @doc capture sidecar (set-and-consume;
	// no-op when capture is off): the delivered CLAUDE.md is the marker-bearing
	// proof — present for a positive scenario, absent for a withheld/retracted/
	// revoked one — that no CLI stdout carries.
	w.docStepMaterialized = body
	return body, nil
}

// j001500AssertGeneratedSettingsContains/DoesNotContain check the GENERATED
// executable-surface files under the materialize target — the delivery path
// distinct from content assembly, and the whole point of scenarios 3-4 (task
// brief: "assert the actual GENERATED settings file").
func j001500AssertMCPPresence(w *World, present bool) error {
	return j001500AssertGeneratedFile(w, filepath.Join("out", ".mcp.json"), j001500MCPMarker, present)
}

func j001500AssertHookPresence(w *World, present bool) error {
	return j001500AssertGeneratedFile(w, filepath.Join("out", ".claude", "settings.json"), j001500HookMarker, present)
}

func j001500AssertGeneratedFile(w *World, rel, marker string, present bool) error {
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read generated %s: %w", rel, err)
	}
	// Surface the generated executable-surface file (.mcp.json /
	// settings.json) to the @doc capture sidecar — the delivery proof for the
	// MCP-server/hook scenarios, which lives in a file rather than any stdout.
	w.docStepMaterialized = body
	has := strings.Contains(body, marker)
	if present && !has {
		return fmt.Errorf("generated %s does not contain %q; content:\n%s", rel, marker, body)
	}
	if !present && has {
		return fmt.Errorf("generated %s unexpectedly contains %q; content:\n%s", rel, marker, body)
	}
	return nil
}

func registerJ001500Steps(ctx *godog.ScenarioContext) {
	// --- Background ----------------------------------------------------------

	ctx.Step(`^Trent's company publishes a "([^"]*)" bundle, signed with the company key$`, func(c context.Context, bundleName string) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		j001500 := j001500Of(w)
		j001500.bundleName = bundleName
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate company signer: %w", err)
		}
		j001500.signer = signer
		root := treeBundlePath(bundleName)
		url, err := w.env.SeedSignedTreeRemote(root, bundleName, j001500TreeEnvelope, j001500TreeItems(j001500CompanyMarker, false), signer)
		if err != nil {
			return fmt.Errorf("seed signed company remote: %w", err)
		}
		j001500.url = url
		j001500.bare = strings.TrimPrefix(url, "file://")
		return nil
	})

	ctx.Step(`^Alice trusts the company key$`, func(c context.Context) error {
		w := worldFrom(c)
		j001500 := j001500Of(w)
		j001500.principal = "trent@example.com"
		pubBytes := ssh.MarshalAuthorizedKey(j001500.signer.Public)
		keyPath := filepath.Join(w.env.Root, "company-signer.pub")
		if err := os.WriteFile(keyPath, pubBytes, 0o644); err != nil {
			return fmt.Errorf("write company public key: %w", err)
		}
		// Drives the real "ctxloom signer trust" leaf (project store, so the
		// whole team inherits the trust decision) — see completeness_test.go's
		// knownUncoveredCLI, pruned for this ref by J001500.
		return runOK(w, "signer", "trust", j001500.principal, "--key", keyPath, "--project")
	})

	// --- Scenario 1: reference mechanic ---------------------------------------

	ctx.Step(`^Alice references the company's secure-coding bundle from her project$`, func(c context.Context) error {
		return j001500EnsureReferenced(worldFrom(c))
	})

	// "Alice starts a session" is already registered by steps_j000200_setup.go
	// (materialize "default" into "out") — reused as-is rather than duplicated
	// (godog rejects an ambiguous second match for the same step text). Every
	// J001500 scenario that reaches this step has already wired+pulled the company
	// bundle in its own preceding Given/When step (j001500EnsureReferenced), so
	// nothing J001500-specific needs to happen here.

	ctx.Step(`^her assistant receives the company's secure-coding guidance, because the company key signed it$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j001500ReadMaterialized(w)
		if err != nil {
			return err
		}
		if !strings.Contains(body, j001500CompanyMarker) {
			return fmt.Errorf("materialized context does not contain the company's guidance marker; content:\n%s", body)
		}
		return nil
	})

	// --- Scenario 2: TAMPER ----------------------------------------------------

	ctx.Step(`^Mallory alters the company's secure-coding bundle after it was signed$`, func(c context.Context) error {
		w := worldFrom(c)
		j001500 := j001500Of(w)
		root := treeBundlePath(j001500.bundleName)
		// AdvanceRemote (not AdvanceSignedTreeRemote): the fragment file's
		// bytes change but the OLD SHA256SUMS/.sigs — signed over the
		// ORIGINAL content — survive untouched, so they no longer cover these
		// new bytes. That mismatch is what attest.VerifyBundle's Contents
		// check (and, for a local read, treeIntegrityFacts) reports as
		// tampering — the tree-form analogue of a stale detached `.sig`.
		if err := w.env.AdvanceRemote(j001500.bare, map[string]string{root + "/fragments/guidance.md": j001500TamperedMarker}); err != nil {
			return fmt.Errorf("advance remote with tampered content: %w", err)
		}
		// Wire (but do not pull) the reference: scenario 2 has no antecedent
		// "Alice references..." step, so her first-ever sync below pulls
		// straight into the already-tampered HEAD.
		return j001500WireReference(w)
	})

	ctx.Step(`^Alice syncs her project$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001500EnsureReferenced(w); err != nil {
			return err
		}
		_ = w.env.Run("profile", "materialize", "default", "--target", "out")
		return nil
	})

	// The tamper scenario's sync. Unlike "Alice syncs her project" the pull is
	// EXPECTED to fail: a pull verifies what it fetched before pinning it, so a
	// tree whose files no longer match their signed SHA256SUMS is refused at
	// the pull, never installed. The materialize still runs so the delivered
	// context can be read for the altered marker.
	ctx.Step(`^Alice tries to sync her project$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001500Of(w)
		if err := j001500WireReference(w); err != nil {
			return err
		}
		_ = w.env.Run("deps", "pull")
		st.tamperPullOutput, st.tamperPullExit = w.env.LastOutput(), w.env.LastExitCode()
		_ = w.env.Run("profile", "materialize", "default", "--target", "out")
		return nil
	})

	ctx.Step(`^the sync refuses to install the altered bundle$`, func(c context.Context) error {
		st := j001500Of(worldFrom(c))
		if st.tamperPullExit == 0 {
			return fmt.Errorf("`deps pull` of a tree altered after signing succeeded; output:\n%s", st.tamperPullOutput)
		}
		return nil
	})

	ctx.Step(`^her assistant does not receive the altered guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j001500ReadMaterialized(w)
		if err != nil {
			return err
		}
		if strings.Contains(body, j001500TamperedMarker) {
			return fmt.Errorf("materialized context unexpectedly contains the tampered marker; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Alice is warned that the content's signature does not verify$`, func(c context.Context) error {
		w := worldFrom(c)
		out := j001500Of(w).tamperPullOutput
		w.docStepMaterialized = strings.TrimSpace(out)
		// The warning has to say the ATTESTATION is the problem, and it has to
		// name the item — "something was withheld" is not a diagnosis. For a
		// TREE bundle (this fixture) the wording is ErrTreeBundleWithheld's
		// (internal/core/bundles/reader_repofs.go's verifyTree, wrapping
		// attest.VerifyBundle's Contents mismatch), not bundles.Reason.Explain's
		// per-item ReasonTampered rendering: a tree-form tamper is caught at
		// the PULL, before any item is individually classified, so the whole
		// bundle is withheld rather than one item — a genuine architectural
		// difference from the single-document form, not a wording preference.
		if !strings.Contains(out, "does not match what was signed") {
			return fmt.Errorf("`deps pull` output does not warn that the tree's content does not match what was signed; output:\n%s", out)
		}
		if !strings.Contains(out, "fragments/guidance.md") {
			return fmt.Errorf("`deps pull` output does not name the altered file; output:\n%s", out)
		}
		return nil
	})

	// --- Scenarios 3 & 4: EXECUTABLE trust gate --------------------------------

	ctx.Step(`^the company's bundle ships an MCP server and a hook$`, func(c context.Context) error {
		w := worldFrom(c)
		j001500 := j001500Of(w)
		root := treeBundlePath(j001500.bundleName)
		if err := w.env.AdvanceSignedTreeRemote(j001500.bare, root, j001500.bundleName, j001500TreeEnvelope, j001500TreeItems(j001500CompanyMarker, true), j001500.signer); err != nil {
			return fmt.Errorf("advance remote with mcp+hook: %w", err)
		}
		return j001500EnsureReferenced(w)
	})

	ctx.Step(`^Alice has rejected the hook$`, func(c context.Context) error {
		w := worldFrom(c)
		j001500 := j001500Of(w)
		ref := canonicalItemRef(j001500.url, j001500.bundleName, "hooks/session_start/0")
		return runOK(w, "bundle", "reject", ref)
	})

	ctx.Step(`^the MCP server appears in her assistant's configuration$`, func(c context.Context) error {
		return j001500AssertMCPPresence(worldFrom(c), true)
	})

	ctx.Step(`^the hook appears in her assistant's configuration$`, func(c context.Context) error {
		return j001500AssertHookPresence(worldFrom(c), true)
	})

	ctx.Step(`^the MCP server still appears in her configuration$`, func(c context.Context) error {
		return j001500AssertMCPPresence(worldFrom(c), true)
	})

	ctx.Step(`^the hook is absent, because she rejected it$`, func(c context.Context) error {
		return j001500AssertHookPresence(worldFrom(c), false)
	})

	// --- Scenario 5: RETRACTION — @wip, see the feature file's comment --------
	// (internal/adapters/remote/retract.go's CheckRetracted is only ever consulted by
	// Puller.confirmRetraction, and operations.syncItem — the only caller —
	// either skips already-installed refs before Pull ever runs, or hardcodes
	// Force:true when it does. EffectiveTrust never consults retraction at
	// all. Retraction currently has NO effect on already-distributed content
	// through any CLI path — see this journey's final report.)

	ctx.Step(`^Alice already receives the company's secure-coding guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001500StartSession(w); err != nil {
			return err
		}
		body, err := j001500ReadMaterialized(w)
		if err != nil {
			return err
		}
		if !strings.Contains(body, j001500CompanyMarker) {
			return fmt.Errorf("setup failed: Alice does not yet receive the company's guidance; content:\n%s", body)
		}
		return nil
	})

	// A retraction is a new SIGNED release: its bundle.yaml withdraws the
	// bundle, and the retraction check reads only the signed tip manifest.
	ctx.Step(`^Trent retracts that version of the bundle$`, func(c context.Context) error {
		w := worldFrom(c)
		j001500 := j001500Of(w)
		root := treeBundlePath(j001500.bundleName)
		envelope := "version: 1.1.0\nwithdrawn: found to be incorrect guidance; do not use\n"
		return w.env.AdvanceSignedTreeRemote(j001500.bare, root, j001500.bundleName, envelope, j001500TreeItems(j001500CompanyMarker, false), j001500.signer)
	})

	ctx.Step(`^Alice is told the content was retracted$`, func(c context.Context) error {
		w := worldFrom(c)
		// The retraction notice lives in the pull's OWN output — the run
		// immediately before the materialize that followed it in "Alice
		// syncs her project", so NthLastOutput(1) reaches it even though
		// LastOutput() now reflects the later materialize.
		out := w.env.NthLastOutput(1)
		w.docStepMaterialized = out
		if !strings.Contains(out, "retracted") {
			return fmt.Errorf("sync output does not mention retraction; output:\n%s", out)
		}
		return nil
	})

	ctx.Step(`^her assistant no longer receives it$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j001500ReadMaterialized(w)
		if err != nil {
			return err
		}
		if strings.Contains(body, j001500CompanyMarker) {
			return fmt.Errorf("materialized context still contains the retracted guidance; content:\n%s", body)
		}
		return nil
	})

	// --- Scenario 6: KEY REVOCATION --------------------------------------------

	ctx.Step(`^Alice receives several bundles the company signed with its key$`, func(c context.Context) error {
		w := worldFrom(c)
		j001500 := j001500Of(w)
		if err := j001500EnsureReferenced(w); err != nil { // the background's "secure-coding" bundle
			return err
		}
		for _, extra := range []struct{ name, marker string }{
			{"extra-a", j001500ExtraMarkerA},
			{"extra-b", j001500ExtraMarkerB},
		} {
			root := treeBundlePath(extra.name)
			url, err := w.env.SeedSignedTreeRemote(root, extra.name, j001500TreeEnvelope, j001500TreeItems(extra.marker, false), j001500.signer)
			if err != nil {
				return fmt.Errorf("seed signed extra bundle %q: %w", extra.name, err)
			}
			remoteName := "company-" + extra.name
			if err := runOK(w, "remote", "create", remoteName, url, "--forge", "git"); err != nil {
				return err
			}
			if err := runOK(w, "profile", "modify", "default", "--add-bundle", remoteName+"/"+extra.name); err != nil {
				return err
			}
			j001500.extraMarkers = append(j001500.extraMarkers, extra.marker)
		}
		return runOK(w, "deps", "pull")
	})

	ctx.Step(`^the company key is compromised$`, func(c context.Context) error {
		// Narrative beat only — the consequence is what the next two steps
		// (revoking trust, then syncing) actually drive and assert.
		return nil
	})

	ctx.Step(`^Alice revokes her trust in the company key$`, func(c context.Context) error {
		w := worldFrom(c)
		// Drives the real "ctxloom signer untrust" leaf — see
		// completeness_test.go's knownUncoveredCLI, pruned for this ref by J001500.
		return runOK(w, "signer", "untrust", j001500Of(w).principal, "--project")
	})

	ctx.Step(`^her assistant no longer receives any content signed by that key$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j001500ReadMaterialized(w)
		if err != nil {
			return err
		}
		markers := append([]string{j001500CompanyMarker}, j001500Of(w).extraMarkers...)
		for _, m := range markers {
			if strings.Contains(body, m) {
				return fmt.Errorf("materialized context still contains %q after revoking the company key; content:\n%s", m, body)
			}
		}
		return nil
	})

	ctx.Step(`^that content is held for her review, as if it had never been signed$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runOK(w, "review", "--list"); err != nil {
			return err
		}
		out := w.env.LastStdout()
		for _, want := range []string{j001500Of(w).bundleName, "guidance", "new"} {
			if !strings.Contains(out, want) {
				return fmt.Errorf("review --list does not show the formerly-signed content as pending %q; stdout:\n%s", want, out)
			}
		}
		return nil
	})

	// The withheld advisory "Alice syncs her project" printed (its materialize is
	// the last run) must give the SIGNER as the reason. The expected sentence is
	// the reason's own rendering, so the step follows the wording wherever it
	// is changed; what it pins is WHICH reason was chosen, and "awaiting review"
	// alone is shared by the unsigned and pending reasons, so it cannot tell
	// them apart.
	ctx.Step(`^Alice is told the content is held because this machine no longer trusts the key that signed it$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = strings.TrimSpace(out)
		want := bundles.ReasonUntrustedSigner.Explain("")
		if !strings.Contains(out, want) {
			return fmt.Errorf("sync output does not give the untrusted signing key as the reason content is held (want %q); output:\n%s", want, out)
		}
		return nil
	})

	ctx.Step(`^Alice references a bundle nobody signed, shipping guidance, an MCP server and a hook$`, func(c context.Context) error {
		w := worldFrom(c)
		root := treeBundlePath(j001500UnsignedBundle)
		files := map[string]string{root + "/" + bundles.DirectoryFormManifest: j001500TreeEnvelope}
		for rel, body := range j001500TreeItems(j001500UnsignedMarker, true) {
			files[root+"/"+rel] = body
		}
		url, err := w.env.SeedRemote(files)
		if err != nil {
			return fmt.Errorf("seed unsigned bundle remote: %w", err)
		}
		if err := runOK(w, "remote", "create", j001500UnsignedRemote, url, "--forge", "git"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", j001500UnsignedRemote+"/"+j001500UnsignedBundle); err != nil {
			return err
		}
		return runOK(w, "deps", "pull")
	})

	// Reads the per-item withheld advisory the sync's materialize printed. An
	// executable's line must say more than the bare "awaiting review" sentence
	// (what would admit it), and the fragment's must not: its reason already
	// says everything. Both sides are measured against the reason's own
	// rendering rather than a copy of the detail's wording.
	ctx.Step(`^Alice is told, item by item and in name order, why each piece of it is held, and what would admit each executable$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = strings.TrimSpace(out)
		bare := bundles.ReasonPending.Explain("")
		var refs []string
		for _, line := range j001500WithheldLine.FindAllStringSubmatch(out, -1) {
			ref, reason := line[1], line[2]
			if ref == "" {
				return fmt.Errorf("a withheld line names no item: %q; output:\n%s", line[0], out)
			}
			if !strings.Contains(ref, j001500UnsignedBundle) || slices.Contains(refs, ref) {
				continue
			}
			refs = append(refs, ref)
			if err := j001500CheckHeldReason(ref, reason, bare); err != nil {
				return fmt.Errorf("%w; output:\n%s", err, out)
			}
		}
		if len(refs) != 3 {
			return fmt.Errorf("want a withheld line for each of the unsigned bundle's 3 items, got %d distinct (%v); output:\n%s", len(refs), refs, out)
		}
		if !slices.IsSorted(refs) {
			return fmt.Errorf("withheld items are not listed in name order: %v; output:\n%s", refs, out)
		}
		return nil
	})

	// --- Scenario 7: FORGERY PRIMITIVE ------------------------------------------

	ctx.Step(`^Alice has no signing key available$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		// Neutralize any AMBIENT ssh-agent on the machine running this suite:
		// isolatedEnv() replaces HOME/XDG_* and scrubs ctxloom session vars, but
		// SSH_AUTH_SOCK is neither — a developer's real agent would otherwise
		// leak in and satisfy key discovery, making this scenario flaky/host-
		// dependent instead of a deterministic "no key anywhere" case.
		w.env.SetEnv("SSH_AUTH_SOCK", "")

		// One unsigned, untrusted pending item so `review --project` has
		// something to act on: an empty pending set short-circuits ("Nothing is
		// pending review.") before ever resolving a signer.
		root := treeBundlePath("bystander")
		files := map[string]string{root + "/" + bundles.DirectoryFormManifest: j001500TreeEnvelope}
		for rel, body := range j001500TreeItems(j001500ForgeryMarker, false) {
			files[root+"/"+rel] = body
		}
		url, err := w.env.SeedRemote(files)
		if err != nil {
			return fmt.Errorf("seed unsigned bystander remote: %w", err)
		}
		if err := runOK(w, "remote", "create", "bystander", url, "--forge", "git"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", "bystander/bystander"); err != nil {
			return err
		}
		return runOK(w, "deps", "pull")
	})

	ctx.Step(`^Alice tries to record a review decision into the team's shared store$`, func(c context.Context) error {
		w := worldFrom(c)
		// `ctxloom review` (sans --list) only reaches resolveReviewSigner on a
		// REAL tty (isInteractiveTerminal()) — a plain piped Run/RunWithStdin
		// would take the non-interactive list branch and never exercise this
		// path at all, mirroring steps_j000200_common.go's driveDiscoverySessionViaMock.
		sess, err := w.env.RunPTY(100, 30, nil, "review", "--project")
		if err != nil {
			return fmt.Errorf("start 'ctxloom review --project' pty: %w", err)
		}
		defer sess.Close()
		exited, waitErr := sess.Wait(10 * time.Second)
		if !exited {
			return fmt.Errorf("'ctxloom review --project' did not exit within timeout; captured output:\n%s", sess.Output())
		}
		_ = waitErr // the exit code (checked below) is the authoritative signal
		j001500 := j001500Of(w)
		j001500.reviewPTYOutput = sess.Output()
		j001500.reviewPTYExit = sess.ExitCode()
		// The refusal ("no signing key available") is PTY output, invisible to
		// the @doc sidecar's w.env view — surface it as this step's evidence.
		w.docStepMaterialized = j001500.reviewPTYOutput
		return nil
	})

	ctx.Step(`^ctxloom refuses, because a team decision must be signed$`, func(c context.Context) error {
		w := worldFrom(c)
		j001500 := j001500Of(w)
		// j001500.reviewPTYOutput was already attached to "Alice tries to record a
		// review decision..." via the set-and-consume docStepMaterialized
		// field, which that step already spent — this Then re-checks the same
		// PTY transcript, so re-attach it (plus the exit code the assertion is
		// actually keyed on) rather than leave this step's pane empty.
		w.docStepMaterialized = fmt.Sprintf("'ctxloom review --project' exit=%d:\n%s", j001500.reviewPTYExit, strings.TrimSpace(j001500.reviewPTYOutput))
		if j001500.reviewPTYExit == 0 {
			return fmt.Errorf("expected 'ctxloom review --project' to refuse (non-zero exit) with no signing key available, got exit 0; output:\n%s", j001500.reviewPTYOutput)
		}
		if !strings.Contains(j001500.reviewPTYOutput, "no signing key available") {
			return fmt.Errorf("output does not explain the missing signing key; output:\n%s", j001500.reviewPTYOutput)
		}
		return nil
	})

	ctx.Step(`^nothing is written to the team store$`, func(c context.Context) error {
		w := worldFrom(c)
		// A negative assertion (no file was written) has no file content to
		// show — the real, observed evidence for it is the directory listing
		// that .ctxloom/approvals is absent from.
		entries, _ := os.ReadDir(filepath.Join(w.env.ProjectDir, ".ctxloom"))
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		w.docStepMaterialized = fmt.Sprintf(".ctxloom/ contents after the refused review (no \"approvals\"): %s", strings.Join(names, ", "))
		if w.env.FileExists(".ctxloom/approvals") {
			return fmt.Errorf("the team store .ctxloom/approvals unexpectedly exists after a refused 'ctxloom review --project'")
		}
		return nil
	})
}
