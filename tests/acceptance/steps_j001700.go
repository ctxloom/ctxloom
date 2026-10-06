//go:build acceptance

// J001700: the "incident" journey (j001700_incident.feature) — a bad command ships and
// must be pulled. J001500 (j001500_corporate_signed.feature)
// already has LOCKED, green scenarios for single-developer retraction and
// company-key revocation, so this journey keeps only the two things J001500
// cannot express — retraction propagating across MORE THAN ONE
// already-installed developer, and ctxloom's own go:embed'd publisher key:
// though it might look irrevocable and invisible, the key is
// visible, and locally revocable — it is surfaced by `signer
// list`/`show` (tagged embedded/not-removable) and `signer untrust` aimed at
// it persists a real local suppression the trust root honors, though the
// compiled-in bytes themselves still only change via a new binary (see the
// feature file's own comments for the full rationale).
//
// Cast: Carol (team lead, reused from steps_j000700_team.go's persona — this is
// exactly the "team lead shares a command" shape, just with a remote/signed
// bundle instead of first-party content), Bob (teammate, j000700State.bobDir — a
// genuinely separate clone, NOT a new persona model per the task brief),
// Trent (the company/trusted publisher, reusing J001500's signing primitives:
// testenv.TestSigner/SeedSignedTreeRemote/AdvanceRemote), Alice (developer,
// scenario 2 only, whose project scaffold scenario 2 needs but whose
// persona plays no other role in it).
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
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
	// (internal/core/config/embedded_signers.allowed_signers) — scenario 2 targets
	// this REAL identity, not a stand-in, so the finding is about the actual
	// production trust root.
	j001700EmbeddedPrincipal = "ben+ctxloom@abbitt.me"
)

// j001700State is this journey's fixture state.
type j001700State struct {
	companyBare   string              // bare repo path (no file:// prefix) for the company's signed bundle remote, for AdvanceRemote
	companySigner *testenv.TestSigner // the company's publishing key, which signs its retracting release

	// Carol's and Bob's retraction-sync {pull, materialize} outputs are read
	// off their own run histories (w.env / w.j000700().bobRuns) at assertion
	// time — no snapshot fields needed, since each developer's history is
	// their own and nothing else runs in it between their sync and the Then.

	embeddedShowBefore   string // `signer show <embedded principal>` output BEFORE the removal attempt
	embeddedRemoveOutput string // `signer untrust <embedded principal> --project` output
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

// j001700TreeEnvelope/j001700TreeItems render the company's bundle as a true
// tree — one fragment named "guidance" whose content is marker — mirroring
// j001500TreeEnvelope/j001500TreeItems exactly (same shape, new marker)
// rather than reusing J001500's constant, since J001700's content is
// thematically distinct (an incident runbook, not secure-coding guidance)
// even though the underlying mechanism is identical.
const j001700TreeEnvelope = "version: \"1.0.0\"\n"

func j001700TreeItems(marker string) map[string]string {
	return map[string]string{"fragments/guidance.md": marker}
}

func registerJ001700Steps(ctx *godog.ScenarioContext) {
	// --- Scenario 1: retraction across more than one already-installed developer ---

	// --- Scenario 3: retraction survives the remote going unreachable ---------

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
			return w.env.LastStdout(), formatAskedFor(w)
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
				return fmt.Errorf("'signer untrust' must no longer report a bare \"no entry for\" for the embedded principal — it has a real local-suppression effect now; output:\n%s", j001700.embeddedRemoveOutput)
			}
			if !strings.Contains(j001700.embeddedRemoveOutput, "cannot be deleted") {
				return fmt.Errorf("expected 'signer untrust' to say the embedded key cannot be deleted; output:\n%s", j001700.embeddedRemoveOutput)
			}
			if !strings.Contains(j001700.embeddedRemoveOutput, "DISTRUSTED") {
				return fmt.Errorf("expected 'signer untrust' to report the embedded key is now DISTRUSTED locally; output:\n%s", j001700.embeddedRemoveOutput)
			}
			return nil
		}
		// The structured payload states the same two facts by field rather
		// than by sentence: EmbeddedSuppressed is operations.RemoveSignerResult's
		// "the compiled-in key cannot be deleted, but a local suppression was
		// recorded" flag, and SuppressionPath is where that record landed.
		suppressed, err := jsonAtPathFrom(j001700.embeddedRemoveOutput, "embedded_suppressed")
		if err != nil {
			return fmt.Errorf("%w; output:\n%s", err, j001700.embeddedRemoveOutput)
		}
		if got, _ := jsonScalar(suppressed); got != "true" {
			return fmt.Errorf("json EmbeddedSuppressed = %q, want %q (the embedded key cannot be deleted); output:\n%s", got, "true", j001700.embeddedRemoveOutput)
		}
		suppressionPath, err := jsonAtPathFrom(j001700.embeddedRemoveOutput, "suppression_path")
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
		if err := j001700CheckEmbeddedShow(j001700.embeddedShowBefore, j001700.embeddedShowBeforeFormat, false, "before"); err != nil {
			return err
		}
		if err := j001700CheckEmbeddedShow(j001700.embeddedShowAfter, j001700.embeddedShowAfterFormat, true, "after"); err != nil {
			return err
		}
		return nil
	})
}

// j001700CheckEmbeddedShow checks one 'signer show' snapshot of the embedded
// principal: listed, tagged embedded, and suppressed exactly when
// wantSuppressed — read from text or JSON as format says.
func j001700CheckEmbeddedShow(raw string, format clifmt.Format, wantSuppressed bool, label string) error {
	if !format.Structured() {
		return j001700CheckEmbeddedShowText(raw, wantSuppressed, label)
	}
	return j001700CheckEmbeddedShowJSON(raw, wantSuppressed, label)
}

// j001700CheckEmbeddedShowText is j001700CheckEmbeddedShow for a text snapshot.
func j001700CheckEmbeddedShowText(raw string, wantSuppressed bool, label string) error {
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

// j001700CheckEmbeddedShowJSON is j001700CheckEmbeddedShow for a JSON snapshot.
func j001700CheckEmbeddedShowJSON(raw string, wantSuppressed bool, label string) error {
	// operations.ShowSigner narrows to the one listing for the
	// requested principal, so the JSON payload is a one-element array —
	// the same shape "0.entry.principals.0"/"0.source"/"0.suppressed"
	// addresses for both the before and after snapshot.
	principal, err := jsonAtPathFrom(raw, "0.entry.principals.0")
	if err != nil {
		return fmt.Errorf("%s: %w; output:\n%s", label, err, raw)
	}
	if got, _ := jsonScalar(principal); got != j001700EmbeddedPrincipal {
		return fmt.Errorf("'signer show' (%s) json principal = %q, want %q; output:\n%s", label, got, j001700EmbeddedPrincipal, raw)
	}
	source, err := jsonAtPathFrom(raw, "0.source")
	if err != nil {
		return fmt.Errorf("%s: %w; output:\n%s", label, err, raw)
	}
	if got, _ := jsonScalar(source); got != "embedded" {
		return fmt.Errorf("'signer show' (%s) json source = %q, want %q; output:\n%s", label, got, "embedded", raw)
	}
	suppressed, err := jsonAtPathFrom(raw, "0.suppressed")
	if err != nil {
		return fmt.Errorf("%s: %w; output:\n%s", label, err, raw)
	}
	want := fmt.Sprintf("%v", wantSuppressed)
	if got, _ := jsonScalar(suppressed); got != want {
		return fmt.Errorf("'signer show' (%s) json suppressed = %s, want %s; output:\n%s", label, got, want, raw)
	}
	return nil
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

// assertToldOfRetraction checks that a developer's routine sync — a `deps
// pull` then a `profile materialize`, the two newest entries in THEIR OWN run
// history — told them about the retraction, and attaches both outputs as the
// step's evidence. Each output is anchored on something only that command
// prints: the pull's "Retracted: N" summary line and the materialize's JSON
// report (operations.MaterializeProfileResult on stdout). The withheld
// warning alone cannot tell them apart — both commands emit it — so a check
// on that would pass with the two registers swapped, or with one of them
// stale from an earlier command.
func assertToldOfRetraction(w *World, who string, runs *testenv.RunHistory) error {
	syncOutput := runs.NthLastOutput(1)
	materialized := runs.LastOutput()
	w.docStepMaterialized = syncOutput + "\n" + materialized
	if !strings.Contains(syncOutput, "Retracted: 1") {
		return fmt.Errorf("the sync output for %s does not report the retraction; output:\n%s", who, syncOutput)
	}
	var report operations.MaterializeProfileResult
	if err := json.Unmarshal([]byte(runs.LastStdout()), &report); err != nil || len(report.Wrote) == 0 {
		return fmt.Errorf("the newest command for %s did not report as a materialize (err=%v); stdout:\n%s", who, err, runs.LastStdout())
	}
	wantWarn := fmt.Sprintf("retracted by the publisher (%s)", j001700RetractReason)
	if !strings.Contains(materialized, wantWarn) {
		return fmt.Errorf("the materialize output for %s does not carry the exact withheld reason %q; output:\n%s", who, wantWarn, materialized)
	}
	return nil
}
