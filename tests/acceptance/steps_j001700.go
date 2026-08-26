//go:build acceptance

// J001700: the "incident" journey (j001700_incident.feature) — a bad command ships and
// must be pulled. J001500 (j001500_corporate_signed.feature)
// already has LOCKED, green scenarios for single-developer retraction and
// company-key revocation, so this journey keeps only the two things J001500
// cannot express — retraction propagating across MORE THAN ONE
// already-installed developer, and ctxloom's own go:embed'd publisher key:
// though it might look irrevocable and invisible, the key is
// visible, and locally revocable — it is surfaced by `signer
// list`/`show` (tagged embedded/not-removable) and `signer remove` aimed at
// it persists a real local suppression TrustRoot() honors, though the
// compiled-in bytes themselves still only change via a new binary (see the
// feature file's own comments for the full rationale).
//
// Cast: Carol (team lead, reused from steps_j000700_team.go's persona — this is
// exactly the "team lead shares a command" shape, just with a remote/signed
// bundle instead of first-party content), Bob (teammate, j000700State.bobDir — a
// genuinely separate clone, NOT a new persona model per the task brief),
// Trent (the company/trusted publisher, reusing J001500's signing primitives:
// testenv.TestSigner/SeedSignedRemote/AdvanceRemote), Alice (developer,
// scenario 2 only, whose project scaffold scenario 2 needs but whose
// persona plays no other role in it).
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// j001700Marker is the company's incident-runbook bundle's distinctive content
	// — the payload scenario 1 asserts reached (and, after retraction, no
	// longer reaches) Carol's and Bob's assembled context.
	j001700Marker = "J001700-INCIDENT-RUNBOOK-MARKER"
	// j001700BundleName is the bundle name Trent retracts — must match the name
	// given in the feature's "Carol leads a team..." step.
	j001700BundleName = "incident-runbook"
	// j001700RetractReason is the publisher's own stated reason, asserted
	// verbatim in the "retracted by the publisher (<reason>)" wording
	// (operations/trust.go:159-177) that reaches both Carol's and Bob's
	// materialize output.
	j001700RetractReason = "shipped an incorrect deploy step; do not use"
	// j001700EmbeddedPrincipal is ctxloom's OWN compiled-in publisher principal
	// (internal/config/embedded_signers.allowed_signers) — scenario 2 targets
	// this REAL identity, not a stand-in, so the finding is about the actual
	// production trust root.
	j001700EmbeddedPrincipal = "ben+ctxloom@abbitt.me"
)

// j001700State is this journey's fixture state.
type j001700State struct {
	companyBare string // bare repo path (no file:// prefix) for the company's signed bundle remote, for AdvanceRemote

	// Carol's own retraction-sync {pull, materialize} outputs are read via
	// w.env.NthLastOutput(1) / .LastOutput() at assertion time —
	// no snapshot fields needed since nothing else runs a command between
	// "Carol runs her next routine sync" and the Then steps below.
	bobSyncOutput   string // Bob's own retraction-sync `deps pull` output (his own runBob-captured stream, separate from w.env — a distinct single-slot register)
	bobMaterialized string // Bob's own retraction-sync `profile materialize` output

	embeddedShowBefore   string // `signer show <embedded principal>` output BEFORE the removal attempt
	embeddedRemoveOutput string // `signer remove <embedded principal> --project` output
	embeddedShowAfter    string // `signer show <embedded principal>` output AFTER the removal attempt

	// The format each of the three calls above actually resolved to — off a
	// terminal (this harness, always) that can differ from row to row of the
	// tabled scenario, so it travels WITH each output snapshot rather than
	// being re-derived from whatever command happens to have run last by the
	// time the Then steps read it.
	embeddedShowBeforeFormat   clifmt.Format
	embeddedRemoveOutputFormat clifmt.Format
	embeddedShowAfterFormat    clifmt.Format
}

// j001700Of returns (lazily creating) this scenario's J001700 fixture state.
func j001700Of(w *World) *j001700State {
	if w.j001700s == nil {
		w.j001700s = &j001700State{}
	}
	return w.j001700s
}

// j001700BundleYAML renders the company's bundle manifest carrying one fragment
// named "guidance" whose content is marker — mirrors j001500BundleYAML exactly
// (same shape, new marker) rather than reusing J001500's constant, since J001700's
// content is thematically distinct (an incident runbook, not secure-coding
// guidance) even though the underlying mechanism is identical.
func j001700BundleYAML(marker string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  guidance:\n    content: %q\n", marker)
}

func registerJ001700Steps(ctx *godog.ScenarioContext) {
	// --- Scenario 1: retraction across more than one already-installed developer ---

	ctx.Step(`^Carol leads a team that already uses the company's "([^"]*)" bundle$`, func(c context.Context, bundleName string) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)

		// Carol's checkout (w.env.ProjectDir) IS the shared team project —
		// mirrors j000700SetupProject's git-init/config/commit/push scaffold, but
		// with a claude-code engine (buildJ000200Config) so materialize writes
		// CLAUDE.md, exactly the surface J001500's mechanism assembles into.
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if _, err := j000700Git(w.env.ProjectDir, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", buildJ000200Config("claude-code", "claude-code")); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "create", "seed", "-d", "J001700 seed bundle"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "create", "default", "-b", "seed", "-d", "J001700 default profile"); err != nil {
			return err
		}

		// Trent's company publishes a SIGNED bundle — reuses J001500's signing
		// primitives directly (TestSigner/SeedSignedRemote), not reinvented.
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate company signer: %w", err)
		}
		rel := ".ctxloom/content/bundles/" + bundleName + ".yaml"
		url, err := w.env.SeedSignedRemote(map[string]string{rel: j001700BundleYAML(j001700Marker)}, []string{rel}, signer)
		if err != nil {
			return fmt.Errorf("seed signed company remote: %w", err)
		}
		j001700.companyBare = strings.TrimPrefix(url, "file://")

		// Carol trusts the company key in the PROJECT store, so it is
		// committed to the team's own git history and Bob inherits it on
		// clone — he never runs `signer add` himself, exactly as a teammate
		// would inherit a lead's trust decision in reality.
		if err := w.env.TrustSigner(signer, "trent@example.com", true); err != nil {
			return fmt.Errorf("trust company signer: %w", err)
		}

		// Wire (but do not yet pull) the reference.
		if err := runOK(w, "remote", "create", "company", url, "--forge", "git"); err != nil {
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
		bare, err := j000700NewBareOrigin(w)
		if err != nil {
			return err
		}
		w.j000700().bareOrigin = bare
		if _, err := j000700Git(w.env.ProjectDir, "remote", "add", "origin", bare); err != nil {
			return err
		}
		if _, err := j000700Git(w.env.ProjectDir, "push", "-u", "origin", "main"); err != nil {
			return err
		}

		// Carol's OWN first pull: her own local install, her own local
		// lockfile — the "already installed" state her retraction-sync step
		// re-checks later.
		if err := runOK(w, "deps", "pull"); err != nil {
			return err
		}
		body, err := materializeDefault(w, "out")
		if err != nil {
			return err
		}
		if !strings.Contains(body, j001700Marker) {
			return fmt.Errorf("setup failed: Carol does not yet have the bundle installed; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Bob has already pulled the team project and installed the bundle too$`, func(c context.Context) error {
		w := worldFrom(c)
		// Bob's OWN separate clone (j000700State.bobDir, steps_j000700_team.go) — a
		// genuinely different checkout inheriting the committed reference and
		// trust decision, then his OWN `deps pull`: his own local install,
		// his own local lockfile, entirely independent of Carol's.
		if err := j000700BobPull(w); err != nil {
			return err
		}
		if err := runBob(w, "deps", "pull"); err != nil {
			return err
		}
		if err := runBob(w, "profile", "materialize", "default", "--target", "out"); err != nil {
			return err
		}
		body, err := readBobFile(w, filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return err
		}
		if !strings.Contains(body, j001700Marker) {
			return fmt.Errorf("setup failed: Bob does not yet have the bundle installed; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Trent retracts the bundle$`, func(c context.Context) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)
		manifest := fmt.Sprintf(
			"version: 1\nretracted:\n  - type: bundle\n    name: %q\n    version: \"\"\n    reason: %q\n",
			j001700BundleName, j001700RetractReason)
		return w.env.AdvanceRemote(j001700.companyBare, map[string]string{".ctxloom/content/manifest.yaml": manifest})
	})

	ctx.Step(`^Carol runs her next routine sync$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runOK(w, "deps", "pull"); err != nil {
			return err
		}
		if _, err := materializeDefault(w, "out"); err != nil {
			return err
		}
		return nil
	})

	ctx.Step(`^Bob runs his next routine sync$`, func(c context.Context) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)
		if err := runBob(w, "deps", "pull"); err != nil {
			return err
		}
		j001700.bobSyncOutput = w.j000700().bobOutput
		if err := runBob(w, "profile", "materialize", "default", "--target", "out"); err != nil {
			return err
		}
		j001700.bobMaterialized = w.j000700().bobOutput
		return nil
	})

	ctx.Step(`^Carol is told the bundle was retracted, and her assistant no longer receives it$`, func(c context.Context) error {
		w := worldFrom(c)
		// "Carol runs her next routine sync" ran pull then materialize, in
		// that order, with nothing else in between: NthLastOutput(1) is the
		// pull's own output, LastOutput() the materialize's — no
		// snapshot fields needed.
		carolSyncOutput := w.env.NthLastOutput(1)
		carolMaterialized := w.env.LastOutput()
		w.docStepMaterialized = carolSyncOutput + "\n" + carolMaterialized
		if !strings.Contains(carolSyncOutput, "retracted") {
			return fmt.Errorf("the sync output for Carol does not mention retraction; output:\n%s", carolSyncOutput)
		}
		wantWarn := fmt.Sprintf("retracted by the publisher (%s)", j001700RetractReason)
		if !strings.Contains(carolMaterialized, wantWarn) {
			return fmt.Errorf("the materialize output for Carol does not carry the exact withheld reason %q; output:\n%s", wantWarn, carolMaterialized)
		}
		body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return fmt.Errorf("read Carol's materialized CLAUDE.md: %w", err)
		}
		if strings.Contains(body, j001700Marker) {
			return fmt.Errorf("the materialized context for Carol still contains the retracted marker; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Bob is told the bundle was retracted too, and his assistant no longer receives it either$`, func(c context.Context) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)
		if !strings.Contains(j001700.bobSyncOutput, "retracted") {
			return fmt.Errorf("the sync output for Bob does not mention retraction; output:\n%s", j001700.bobSyncOutput)
		}
		wantWarn := fmt.Sprintf("retracted by the publisher (%s)", j001700RetractReason)
		if !strings.Contains(j001700.bobMaterialized, wantWarn) {
			return fmt.Errorf("the materialize output for Bob does not carry the exact withheld reason %q; output:\n%s", wantWarn, j001700.bobMaterialized)
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
		if strings.Contains(body, j001700Marker) {
			return fmt.Errorf("the materialized context for Bob still contains the retracted marker; content:\n%s", body)
		}
		w.docStepMaterialized = j001700.bobSyncOutput + "\n" + j001700.bobMaterialized
		return nil
	})

	// --- Scenario 3: retraction survives the remote going unreachable ---------

	ctx.Step(`^the company's remote becomes unreachable$`, func(c context.Context) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)
		// The bare repo IS the remote at this URL (file://<companyBare>) — renaming
		// it away breaks every subsequent fetch/clone against it exactly like a
		// real network partition or outage would: the fetcher gets an
		// undifferentiated failure, indistinguishable at that seam from any other
		// unreachable remote (see internal/remote/retract.go CheckRetracted's doc).
		broken := j001700.companyBare + ".unreachable"
		if err := os.Rename(j001700.companyBare, broken); err != nil {
			return fmt.Errorf("break the company remote: %w", err)
		}
		j001700.companyBare = broken
		return nil
	})

	ctx.Step(`^Carol is told the bundle is still retracted, and her assistant still does not receive it$`, func(c context.Context) error {
		w := worldFrom(c)
		// Same shape as "Carol is told the bundle was retracted..." above:
		// NthLastOutput(1) is the pull's own output, LastOutput() the
		// materialize's — "Carol runs her next routine sync" ran pull
		// then materialize, in that order, with nothing else in between.
		carolSyncOutput := w.env.NthLastOutput(1)
		carolMaterialized := w.env.LastOutput()
		w.docStepMaterialized = carolSyncOutput + "\n" + carolMaterialized
		if !strings.Contains(carolSyncOutput, "retracted") {
			return fmt.Errorf("the sync output for Carol does not mention retraction even though the persisted verdict was retracted; output:\n%s", carolSyncOutput)
		}
		wantWarn := fmt.Sprintf("retracted by the publisher (%s)", j001700RetractReason)
		if !strings.Contains(carolMaterialized, wantWarn) {
			return fmt.Errorf("the materialize output for Carol does not carry the exact withheld reason %q; output:\n%s", wantWarn, carolMaterialized)
		}
		body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return fmt.Errorf("read Carol's materialized CLAUDE.md: %w", err)
		}
		if strings.Contains(body, j001700Marker) {
			return fmt.Errorf("THE SECURITY-CRITICAL CASE: the remote being unreachable let a previously-retracted bundle reach Carol's assembled context again; content:\n%s", body)
		}
		return nil
	})

	// --- Scenario 2: the irrevocable embedded key -----------------------------

	ctx.Step(`^Alice's project exists$`, func(c context.Context) error {
		return ensureProjectWithEngine(worldFrom(c), "claude-code", "claude-code")
	})

	ctx.Step(`^Trent removes ctxloom's own publisher key from the project's trust store, asking for "([^"]*)"$`, func(c context.Context, flags string) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)
		flagArgs, err := shellSplit(flags)
		if err != nil {
			return fmt.Errorf("parse flags %q: %w", flags, err)
		}
		run := func(rest ...string) (string, clifmt.Format) {
			args := append(append([]string{}, flagArgs...), rest...)
			_ = w.env.Run(args...)
			return w.env.LastOutput(), formatAskedFor(w)
		}
		// "before" listing: the embedded principal must already be visible —
		// tagged embedded, not yet distrusted.
		j001700.embeddedShowBefore, j001700.embeddedShowBeforeFormat = run("signer", "show", j001700EmbeddedPrincipal)
		// The removal attempt, aimed straight at the REAL embedded
		// principal — not a stand-in for it. It cannot delete the compiled-in
		// bytes, but it DOES now persist a local suppression.
		j001700.embeddedRemoveOutput, j001700.embeddedRemoveOutputFormat = run("signer", "untrust", j001700EmbeddedPrincipal, "--project")
		// "after" listing: still visible, now tagged locally distrusted.
		j001700.embeddedShowAfter, j001700.embeddedShowAfterFormat = run("signer", "show", j001700EmbeddedPrincipal)
		w.docStepMaterialized = "$ ctxloom " + flags + " signer show " + j001700EmbeddedPrincipal + "\n" + j001700.embeddedShowBefore +
			"\n$ ctxloom " + flags + " signer untrust " + j001700EmbeddedPrincipal + " --project\n" + j001700.embeddedRemoveOutput +
			"\n$ ctxloom " + flags + " signer show " + j001700EmbeddedPrincipal + "\n" + j001700.embeddedShowAfter
		return nil
	})

	ctx.Step(`^ctxloom reports the key cannot be deleted but is now distrusted locally$`, func(c context.Context) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)
		// This Then's OWN evidence: the removal attempt's actual reply. A Then
		// with an empty evidence pane proves nothing to a reader of the
		// published page, however green it is in the suite.
		w.docStepMaterialized = "$ ctxloom signer untrust " + j001700EmbeddedPrincipal + " --project\n" + j001700.embeddedRemoveOutput
		if !j001700.embeddedRemoveOutputFormat.Structured() {
			if strings.Contains(j001700.embeddedRemoveOutput, "no entry for") {
				return fmt.Errorf("'signer remove' must no longer report a bare \"no entry for\" for the embedded principal — it has a real local-suppression effect now; output:\n%s", j001700.embeddedRemoveOutput)
			}
			if !strings.Contains(j001700.embeddedRemoveOutput, "cannot be deleted") {
				return fmt.Errorf("expected 'signer remove' to say the embedded key cannot be deleted; output:\n%s", j001700.embeddedRemoveOutput)
			}
			if !strings.Contains(j001700.embeddedRemoveOutput, "DISTRUSTED") {
				return fmt.Errorf("expected 'signer remove' to report the embedded key is now DISTRUSTED locally; output:\n%s", j001700.embeddedRemoveOutput)
			}
			return nil
		}
		// The structured payload states the same two facts by field rather
		// than by sentence: EmbeddedSuppressed is operations.RemoveSignerResult's
		// "the compiled-in key cannot be deleted, but a local suppression was
		// recorded" flag, and SuppressionPath is where that record landed.
		suppressed, err := jsonAtPathFrom(j001700.embeddedRemoveOutput, "EmbeddedSuppressed")
		if err != nil {
			return fmt.Errorf("%w; output:\n%s", err, j001700.embeddedRemoveOutput)
		}
		if got, _ := jsonScalar(suppressed); got != "true" {
			return fmt.Errorf("json EmbeddedSuppressed = %q, want %q (the embedded key cannot be deleted); output:\n%s", got, "true", j001700.embeddedRemoveOutput)
		}
		suppressionPath, err := jsonAtPathFrom(j001700.embeddedRemoveOutput, "SuppressionPath")
		if err != nil {
			return fmt.Errorf("%w; output:\n%s", err, j001700.embeddedRemoveOutput)
		}
		if got, ok := jsonScalar(suppressionPath); !ok || got == "" {
			return fmt.Errorf("json SuppressionPath is empty, want the path the local distrust record was written to; output:\n%s", j001700.embeddedRemoveOutput)
		}
		return nil
	})

	ctx.Step(`^ctxloom's own signer listing shows that key, tagged embedded and locally distrusted$`, func(c context.Context) error {
		w := worldFrom(c)
		j001700 := j001700Of(w)
		// This Then's OWN evidence: the signer listing before AND after the
		// removal attempt — both name the embedded principal (visibility never
		// regresses), and only the AFTER listing carries the "locally
		// distrusted" tag.
		w.docStepMaterialized = "$ ctxloom signer show " + j001700EmbeddedPrincipal + "   # before the removal attempt\n" + j001700.embeddedShowBefore +
			"\n$ ctxloom signer show " + j001700EmbeddedPrincipal + "   # after the removal attempt\n" + j001700.embeddedShowAfter
		check := func(raw string, format clifmt.Format, wantSuppressed bool, label string) error {
			if !format.Structured() {
				if !strings.Contains(raw, j001700EmbeddedPrincipal) {
					return fmt.Errorf("expected 'signer show' (%s) to list the embedded principal; output:\n%s", label, raw)
				}
				if !strings.Contains(raw, "embedded") {
					return fmt.Errorf("expected 'signer show' (%s) to tag the entry \"embedded\"; output:\n%s", label, raw)
				}
				if got := strings.Contains(raw, "DISTRUSTED"); got != wantSuppressed {
					return fmt.Errorf("'signer show' (%s) reports DISTRUSTED=%v, want %v; output:\n%s", label, got, wantSuppressed, raw)
				}
				return nil
			}
			// operations.ShowSigner narrows to the one listing for the
			// requested principal, so the JSON payload is a one-element array —
			// the same shape "0.Entry.Principals.0"/"0.Source"/"0.Suppressed"
			// addresses for both the before and after snapshot.
			principal, err := jsonAtPathFrom(raw, "0.Entry.Principals.0")
			if err != nil {
				return fmt.Errorf("%s: %w; output:\n%s", label, err, raw)
			}
			if got, _ := jsonScalar(principal); got != j001700EmbeddedPrincipal {
				return fmt.Errorf("'signer show' (%s) json principal = %q, want %q; output:\n%s", label, got, j001700EmbeddedPrincipal, raw)
			}
			source, err := jsonAtPathFrom(raw, "0.Source")
			if err != nil {
				return fmt.Errorf("%s: %w; output:\n%s", label, err, raw)
			}
			if got, _ := jsonScalar(source); got != "embedded" {
				return fmt.Errorf("'signer show' (%s) json Source = %q, want %q; output:\n%s", label, got, "embedded", raw)
			}
			suppressed, err := jsonAtPathFrom(raw, "0.Suppressed")
			if err != nil {
				return fmt.Errorf("%s: %w; output:\n%s", label, err, raw)
			}
			want := fmt.Sprintf("%v", wantSuppressed)
			if got, _ := jsonScalar(suppressed); got != want {
				return fmt.Errorf("'signer show' (%s) json Suppressed = %s, want %s; output:\n%s", label, got, want, raw)
			}
			return nil
		}
		if err := check(j001700.embeddedShowBefore, j001700.embeddedShowBeforeFormat, false, "before"); err != nil {
			return err
		}
		if err := check(j001700.embeddedShowAfter, j001700.embeddedShowAfterFormat, true, "after"); err != nil {
			return err
		}
		return nil
	})
}

// jsonAtPathFrom decodes raw as a JSON document and addresses it with
// jsonAtPath (steps_cli.go) — for a snapshot captured several commands ago,
// where lastOutputStructured's "the LAST command's stdout" premise no longer
// holds.
func jsonAtPathFrom(raw, path string) (any, error) {
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	return jsonAtPath(doc, path)
}
