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
	"strings"

	"github.com/cucumber/godog"
	"golang.org/x/crypto/ssh"

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

// j001500State is this journey's fixture state: the company's signed bundle
// (signer identity, seeded remote, bundle name), whether Alice has wired it
// into her project yet, and the refused tamper pull's outcome.
type j001500State struct {
	signer     *testenv.TestSigner
	principal  string
	url        string // file://<bare> — the seeded remote's clone URL
	bare       string // bare repo path (no file:// prefix), for AdvanceRemote/AdvanceSignedTreeRemote
	bundleName string
	referenced bool // remote added + profile modified (addRemoteBundleBase wiring done)

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

// j001500ReadMaterialized reads out/CLAUDE.md, the assembled content surface.
func j001500ReadMaterialized(w *World) (string, error) {
	body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
	if err != nil {
		return "", fmt.Errorf("read materialized out/CLAUDE.md (materialize output:\n%s): %w", w.env.LastOutput(), err)
	}
	// Surface the assembled context to the @doc capture sidecar (set-and-consume;
	// no-op when capture is off): the delivered CLAUDE.md is the marker-bearing
	// proof — present for a positive scenario, absent for a withheld/revoked
	// one — that no CLI stdout carries.
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
		return runOK(w, "signer", "trust", j001500.principal, "--key", keyPath, "--project", "--yes")
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
		// Text: the steps below read the human warning, not a payload.
		_ = w.env.Run("--format", "text", "deps", "pull")
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

	ctx.Step(`^the MCP server appears in her assistant's configuration$`, func(c context.Context) error {
		return j001500AssertMCPPresence(worldFrom(c), true)
	})

	ctx.Step(`^the hook appears in her assistant's configuration$`, func(c context.Context) error {
		return j001500AssertHookPresence(worldFrom(c), true)
	})

	// --- Scenario 6: KEY REVOCATION --------------------------------------------

	// --- Scenario 7: FORGERY PRIMITIVE ------------------------------------------

}
