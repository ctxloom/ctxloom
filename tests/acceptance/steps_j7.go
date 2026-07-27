//go:build acceptance

// J7: the "incident" journey (j7_incident.feature) — a bad command ships and
// must be pulled. RESCOPED 2026-07-14: J3 (j3_corporate_signed.feature)
// already has LOCKED, green scenarios for single-developer retraction and
// company-key revocation, so this journey keeps only the two things J3
// cannot express — retraction propagating across MORE THAN ONE
// already-installed developer, and ctxloom's own go:embed'd publisher key:
// UPDATED 2026-07-15 (oozy-plod) from "irrevocable and invisible" to
// "visible, and locally revocable" — the key is now surfaced by `signer
// list`/`show` (tagged embedded/not-removable) and `signer remove` aimed at
// it persists a real local suppression TrustRoot() honors, though the
// compiled-in bytes themselves still only change via a new binary (see the
// feature file's own comments for the full rationale).
//
// Cast: Carol (team lead, reused from steps_j2_team.go's persona — this is
// exactly the "team lead shares a command" shape, just with a remote/signed
// bundle instead of first-party content), Bob (teammate, j2State.bobDir — a
// genuinely separate clone, NOT a new persona model per the task brief),
// Trent (the company/trusted publisher, reusing J3's signing primitives:
// testenv.TestSigner/SeedSignedRemote/AdvanceRemote), Alice (developer,
// scenario 2 only, whose project scaffold scenario 2 needs but whose
// persona plays no other role in it).
package acceptance

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// j7Marker is the company's incident-runbook bundle's distinctive content
	// — the payload scenario 1 asserts reached (and, after retraction, no
	// longer reaches) Carol's and Bob's assembled context.
	j7Marker = "J7-INCIDENT-RUNBOOK-MARKER"
	// j7BundleName is the bundle name Trent retracts — must match the name
	// given in the feature's "Carol leads a team..." step.
	j7BundleName = "incident-runbook"
	// j7RetractReason is the publisher's own stated reason, asserted
	// verbatim in the "retracted by the publisher (<reason>)" wording
	// (operations/trust.go:159-177) that reaches both Carol's and Bob's
	// materialize output.
	j7RetractReason = "shipped an incorrect deploy step; do not use"
	// j7EmbeddedPrincipal is ctxloom's OWN compiled-in publisher principal
	// (internal/config/embedded_signers.allowed_signers) — scenario 2 targets
	// this REAL identity, not a stand-in, so the finding is about the actual
	// production trust root.
	j7EmbeddedPrincipal = "ben+ctxloom@abbitt.me"
)

// j7State is this journey's fixture state.
type j7State struct {
	companyBare string // bare repo path (no file:// prefix) for the company's signed bundle remote, for AdvanceRemote

	// Carol's own retraction-sync {pull, materialize} outputs are read via
	// w.env.NthLastOutput(1) / .LastOutput() at assertion time (U163-F01) —
	// no snapshot fields needed since nothing else runs a command between
	// "Carol runs her next routine sync" and the Then steps below.
	bobSyncOutput   string // Bob's own retraction-sync `remote pull` output (his own runBob-captured stream, separate from w.env — a distinct single-slot register, out of U163-F01's scope)
	bobMaterialized string // Bob's own retraction-sync `profile materialize` output

	embeddedShowBefore   string // `signer show <embedded principal>` output BEFORE the removal attempt
	embeddedRemoveOutput string // `signer remove <embedded principal> --project` output
	embeddedShowAfter    string // `signer show <embedded principal>` output AFTER the removal attempt
}

// j7Of returns (lazily creating) this scenario's J7 fixture state.
func j7Of(w *World) *j7State {
	if w.j7s == nil {
		w.j7s = &j7State{}
	}
	return w.j7s
}

// j7BundleYAML renders the company's bundle manifest carrying one fragment
// named "guidance" whose content is marker — mirrors j3BundleYAML exactly
// (same shape, new marker) rather than reusing J3's constant, since J7's
// content is thematically distinct (an incident runbook, not secure-coding
// guidance) even though the underlying mechanism is identical.
func j7BundleYAML(marker string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  guidance:\n    content: %q\n", marker)
}

func registerJ7Steps(ctx *godog.ScenarioContext) {
	// --- Scenario 1: retraction across more than one already-installed developer ---

	ctx.Step(`^Carol leads a team that already uses the company's "([^"]*)" bundle$`, func(c context.Context, bundleName string) error {
		w := worldFrom(c)
		j7 := j7Of(w)

		// Carol's checkout (w.env.ProjectDir) IS the shared team project —
		// mirrors j2SetupProject's git-init/config/commit/push scaffold, but
		// with a claude-code engine (buildJ1Config) so materialize writes
		// CLAUDE.md, exactly the surface J3's mechanism assembles into.
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if _, err := j2Git(w.env.ProjectDir, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", buildJ1Config("claude-code", "claude-code")); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "create", "seed", "-d", "J7 seed bundle"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "create", "default", "-b", "seed", "-d", "J7 default profile"); err != nil {
			return err
		}

		// Trent's company publishes a SIGNED bundle — reuses J3's signing
		// primitives directly (TestSigner/SeedSignedRemote), not reinvented.
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate company signer: %w", err)
		}
		rel := ".ctxloom/content/bundles/" + bundleName + ".yaml"
		url, err := w.env.SeedSignedRemote(map[string]string{rel: j7BundleYAML(j7Marker)}, []string{rel}, signer)
		if err != nil {
			return fmt.Errorf("seed signed company remote: %w", err)
		}
		j7.companyBare = strings.TrimPrefix(url, "file://")

		// Carol trusts the company key in the PROJECT store, so it is
		// committed to the team's own git history and Bob inherits it on
		// clone — he never runs `signer add` himself, exactly as a teammate
		// would inherit a lead's trust decision in reality.
		if err := w.env.TrustSigner(signer, "trent@example.com", true); err != nil {
			return fmt.Errorf("trust company signer: %w", err)
		}

		// Wire (but do not yet pull) the reference.
		if err := runOK(w, "remote", "add", "company", url, "--forge", "git"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", "company/"+bundleName); err != nil {
			return err
		}

		// Commit + push to a fresh team origin BEFORE anyone pulls, so the
		// reference and the trust decision are shared git history but no
		// local lockfile/clone-cache is — each developer's "already
		// installed" state below must be genuinely their own, independently
		// arrived at, never inherited via git.
		if err := w.env.GitCommit("wire the company's " + bundleName + " bundle"); err != nil {
			return err
		}
		bare, err := j2NewBareOrigin(w)
		if err != nil {
			return err
		}
		w.j2().bareOrigin = bare
		if _, err := j2Git(w.env.ProjectDir, "remote", "add", "origin", bare); err != nil {
			return err
		}
		if _, err := j2Git(w.env.ProjectDir, "push", "-u", "origin", "main"); err != nil {
			return err
		}

		// Carol's OWN first pull: her own local install, her own local
		// lockfile — the "already installed" state her retraction-sync step
		// re-checks later.
		if err := runOK(w, "remote", "pull"); err != nil {
			return err
		}
		body, err := materializeDefault(w, "out")
		if err != nil {
			return err
		}
		if !strings.Contains(body, j7Marker) {
			return fmt.Errorf("setup failed: Carol does not yet have the bundle installed; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Bob has already pulled the team project and installed the bundle too$`, func(c context.Context) error {
		w := worldFrom(c)
		// Bob's OWN separate clone (j2State.bobDir, steps_j2_team.go) — a
		// genuinely different checkout inheriting the committed reference and
		// trust decision, then his OWN `remote pull`: his own local install,
		// his own local lockfile, entirely independent of Carol's.
		if err := j2BobPull(w); err != nil {
			return err
		}
		if err := runBob(w, "remote", "pull"); err != nil {
			return err
		}
		if err := runBob(w, "profile", "materialize", "default", "--target", "out"); err != nil {
			return err
		}
		body, err := readBobFile(w, filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return err
		}
		if !strings.Contains(body, j7Marker) {
			return fmt.Errorf("setup failed: Bob does not yet have the bundle installed; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Trent retracts the bundle$`, func(c context.Context) error {
		w := worldFrom(c)
		j7 := j7Of(w)
		manifest := fmt.Sprintf(
			"version: 1\nretracted:\n  - type: bundle\n    name: %q\n    version: \"\"\n    reason: %q\n",
			j7BundleName, j7RetractReason)
		return w.env.AdvanceRemote(j7.companyBare, map[string]string{".ctxloom/content/manifest.yaml": manifest})
	})

	ctx.Step(`^Carol runs her next routine sync$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runOK(w, "remote", "pull"); err != nil {
			return err
		}
		if _, err := materializeDefault(w, "out"); err != nil {
			return err
		}
		return nil
	})

	ctx.Step(`^Bob runs his next routine sync$`, func(c context.Context) error {
		w := worldFrom(c)
		j7 := j7Of(w)
		if err := runBob(w, "remote", "pull"); err != nil {
			return err
		}
		j7.bobSyncOutput = w.j2().bobOutput
		if err := runBob(w, "profile", "materialize", "default", "--target", "out"); err != nil {
			return err
		}
		j7.bobMaterialized = w.j2().bobOutput
		return nil
	})

	ctx.Step(`^Carol is told the bundle was retracted, and her assistant no longer receives it$`, func(c context.Context) error {
		w := worldFrom(c)
		// "Carol runs her next routine sync" ran pull then materialize, in
		// that order, with nothing else in between: NthLastOutput(1) is the
		// pull's own output, LastOutput() the materialize's (U163-F01) — no
		// snapshot fields needed.
		carolSyncOutput := w.env.NthLastOutput(1)
		carolMaterialized := w.env.LastOutput()
		w.docStepMaterialized = carolSyncOutput + "\n" + carolMaterialized
		if !strings.Contains(carolSyncOutput, "retracted") {
			return fmt.Errorf("Carol's sync output does not mention retraction; output:\n%s", carolSyncOutput)
		}
		wantWarn := fmt.Sprintf("retracted by the publisher (%s)", j7RetractReason)
		if !strings.Contains(carolMaterialized, wantWarn) {
			return fmt.Errorf("Carol's materialize output does not carry the exact withheld reason %q; output:\n%s", wantWarn, carolMaterialized)
		}
		body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return fmt.Errorf("read Carol's materialized CLAUDE.md: %w", err)
		}
		if strings.Contains(body, j7Marker) {
			return fmt.Errorf("Carol's materialized context still contains the retracted marker; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Bob is told the bundle was retracted too, and his assistant no longer receives it either$`, func(c context.Context) error {
		w := worldFrom(c)
		j7 := j7Of(w)
		if !strings.Contains(j7.bobSyncOutput, "retracted") {
			return fmt.Errorf("Bob's sync output does not mention retraction; output:\n%s", j7.bobSyncOutput)
		}
		wantWarn := fmt.Sprintf("retracted by the publisher (%s)", j7RetractReason)
		if !strings.Contains(j7.bobMaterialized, wantWarn) {
			return fmt.Errorf("Bob's materialize output does not carry the exact withheld reason %q; output:\n%s", wantWarn, j7.bobMaterialized)
		}
		// readBobFile sets docStepMaterialized itself (to the file it read), so
		// this must run BEFORE the evidence assignment below or it would clobber
		// it — the reader's evidence pane for this step must show Bob being TOLD
		// (the retraction notice + the reasoned withheld advisory), which is the
		// claim the step's own text makes.
		body, err := readBobFile(w, filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return err
		}
		if strings.Contains(body, j7Marker) {
			return fmt.Errorf("Bob's materialized context still contains the retracted marker; content:\n%s", body)
		}
		w.docStepMaterialized = j7.bobSyncOutput + "\n" + j7.bobMaterialized
		return nil
	})

	// --- Scenario 2: the irrevocable embedded key -----------------------------

	ctx.Step(`^Alice's project exists$`, func(c context.Context) error {
		return ensureProjectWithEngine(worldFrom(c), "claude-code", "claude-code")
	})

	ctx.Step(`^Trent removes ctxloom's own publisher key from the project's trust store$`, func(c context.Context) error {
		w := worldFrom(c)
		j7 := j7Of(w)
		// "before" listing: the embedded principal must already be visible
		// (oozy-plod (a)) — tagged embedded, not yet distrusted.
		_ = w.env.Run("signer", "show", j7EmbeddedPrincipal)
		j7.embeddedShowBefore = w.env.LastOutput()
		// The removal attempt, aimed straight at the REAL embedded
		// principal — not a stand-in for it. It cannot delete the compiled-in
		// bytes, but it DOES now persist a local suppression (oozy-plod (b)).
		_ = w.env.Run("signer", "remove", j7EmbeddedPrincipal, "--project")
		j7.embeddedRemoveOutput = w.env.LastOutput()
		// "after" listing: still visible, now tagged locally distrusted.
		_ = w.env.Run("signer", "show", j7EmbeddedPrincipal)
		j7.embeddedShowAfter = w.env.LastOutput()
		w.docStepMaterialized = "$ ctxloom signer show " + j7EmbeddedPrincipal + "\n" + j7.embeddedShowBefore +
			"\n$ ctxloom signer remove " + j7EmbeddedPrincipal + " --project\n" + j7.embeddedRemoveOutput +
			"\n$ ctxloom signer show " + j7EmbeddedPrincipal + "\n" + j7.embeddedShowAfter
		return nil
	})

	ctx.Step(`^ctxloom reports the key cannot be deleted but is now distrusted locally$`, func(c context.Context) error {
		w := worldFrom(c)
		j7 := j7Of(w)
		// This Then's OWN evidence: the removal attempt's actual reply. A Then
		// with an empty evidence pane proves nothing to a reader of the
		// published page, however green it is in the suite.
		w.docStepMaterialized = "$ ctxloom signer remove " + j7EmbeddedPrincipal + " --project\n" + j7.embeddedRemoveOutput
		if strings.Contains(j7.embeddedRemoveOutput, "no entry for") {
			return fmt.Errorf("'signer remove' must no longer report a bare \"no entry for\" for the embedded principal — it has a real local-suppression effect now; output:\n%s", j7.embeddedRemoveOutput)
		}
		if !strings.Contains(j7.embeddedRemoveOutput, "cannot be deleted") {
			return fmt.Errorf("expected 'signer remove' to say the embedded key cannot be deleted; output:\n%s", j7.embeddedRemoveOutput)
		}
		if !strings.Contains(j7.embeddedRemoveOutput, "DISTRUSTED") {
			return fmt.Errorf("expected 'signer remove' to report the embedded key is now DISTRUSTED locally; output:\n%s", j7.embeddedRemoveOutput)
		}
		return nil
	})

	ctx.Step(`^ctxloom's own signer listing shows that key, tagged embedded and locally distrusted$`, func(c context.Context) error {
		w := worldFrom(c)
		j7 := j7Of(w)
		// This Then's OWN evidence: the signer listing before AND after the
		// removal attempt — both name the embedded principal (visibility never
		// regresses), and only the AFTER listing carries the "locally
		// distrusted" tag.
		w.docStepMaterialized = "$ ctxloom signer show " + j7EmbeddedPrincipal + "   # before the removal attempt\n" + j7.embeddedShowBefore +
			"\n$ ctxloom signer show " + j7EmbeddedPrincipal + "   # after the removal attempt\n" + j7.embeddedShowAfter
		for _, out := range []string{j7.embeddedShowBefore, j7.embeddedShowAfter} {
			if !strings.Contains(out, j7EmbeddedPrincipal) {
				return fmt.Errorf("expected 'signer show' to list the embedded principal; output:\n%s", out)
			}
			if !strings.Contains(out, "embedded") {
				return fmt.Errorf("expected 'signer show' to tag the entry \"embedded\"; output:\n%s", out)
			}
		}
		if strings.Contains(j7.embeddedShowBefore, "DISTRUSTED") {
			return fmt.Errorf("the BEFORE listing must not already show the key as locally distrusted; output:\n%s", j7.embeddedShowBefore)
		}
		if !strings.Contains(j7.embeddedShowAfter, "DISTRUSTED") {
			return fmt.Errorf("the AFTER listing must show the key as locally distrusted; output:\n%s", j7.embeddedShowAfter)
		}
		return nil
	})
}
