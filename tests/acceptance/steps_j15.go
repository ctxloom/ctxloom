//go:build acceptance

// J15: the "content my company has validated" journey (j15_corporate_signed.feature)
// — signing/trust as the product's value proposition. Cast: Trent (the
// company/trusted publisher), Alice (the developer), Mallory (the attacker who
// wants to change what reaches Alice's assistant, not read it).
//
// Reuses J2's signing test helpers (tests/integration/testenv/signing_acceptance.go:
// TestSigner, SeedSignedRemote, TrustSigner, AdvanceSignedRemote) and J2's
// scaffolding helpers (steps_j2_common.go: ensureProjectWithEngine, runOK) rather
// than rebuilding trust infrastructure that already exists. World carries exactly
// one new field (j15 *j15State) to hold this journey's fixture state.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// Distinctive marker strings this journey's bundles carry, so a materialized
// context / generated settings file can be checked for exactly the right
// payload (ASSERTION DISCIPLINE) rather than a bare exit-code or file-exists
// proxy.
const (
	j15CompanyMarker  = "J15-COMPANY-SECURECODING-MARKER"
	j15TamperedMarker = "J15-TAMPERED-CONTENT-MARKER"
	j15ExtraMarkerA   = "J15-COMPANY-EXTRA-A-MARKER"
	j15ExtraMarkerB   = "J15-COMPANY-EXTRA-B-MARKER"
	j15HookMarker     = "echo J15-HOOK-EXEC-MARKER"
	j15MCPMarker      = "J15-MCP-EXEC-MARKER"
	j15ForgeryMarker  = "J15-FORGERY-BYSTANDER-MARKER"
)

// j15State is this journey's fixture state: the company's signed bundle
// (signer identity, seeded remote, bundle name), whether Alice has wired it
// into her project yet, and bookkeeping the later scenarios need (rejected
// hook, extra company-signed bundles, the review --project PTY session's
// outcome).
type j15State struct {
	signer     *testenv.TestSigner
	principal  string
	url        string // file://<bare> — the seeded remote's clone URL
	bare       string // bare repo path (no file:// prefix), for AdvanceRemote/AdvanceSignedRemote
	bundleName string
	referenced bool // remote added + profile modified (addRemoteBundleBase wiring done)

	extraMarkers []string // scenario 6: additional company-signed bundles' markers

	reviewPTYOutput string // scenario 7: captured `ctxloom review --project` pty output
	reviewPTYExit   int
}

// j15Of returns (lazily creating) this scenario's J15 fixture state.
func j15Of(w *World) *j15State {
	if w.j15 == nil {
		w.j15 = &j15State{bundleName: "secure-coding"}
	}
	return w.j15
}

// j15BundleYAML renders a bundle manifest carrying one fragment named
// "guidance" whose content IS marker — the payload this journey's content
// scenarios assert reached (or was withheld from) the assembled context.
func j15BundleYAML(marker string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  guidance:\n    content: %q\n", marker)
}

// j15BundleYAMLWithExec is j15BundleYAML plus an MCP server and a session-start
// hook, each carrying its own distinctive marker in its command — the payload
// the executable-trust-gate scenarios (3, 4) assert reached (or was withheld
// from) the GENERATED settings files (.mcp.json / .claude/settings.json),
// distinct from the content-delivery path.
func j15BundleYAMLWithExec(marker string) string {
	return fmt.Sprintf(
		"version: \"1.0.0\"\nfragments:\n  guidance:\n    content: %q\n"+
			"mcp:\n  demo-server:\n    command: %q\n    args: [%q]\n"+
			"hooks:\n  session_start:\n    - command: %q\n      type: command\n",
		marker, "/bin/echo", j15MCPMarker, j15HookMarker)
}

// j15WireReference adds the company's remote and references its bundle from
// the "default" profile — WITHOUT pulling. Idempotent: a second call is a
// no-op. Split from j15EnsureReferenced so the tamper scenario (which has no
// antecedent "Alice references..." step) can wire the reference before
// Mallory's tampered commit exists, and only pull afterward.
func j15WireReference(w *World) error {
	j15 := j15Of(w)
	if j15.referenced {
		return nil
	}
	if j15.url == "" {
		return fmt.Errorf("the company's bundle was never seeded")
	}
	if err := runOK(w, "remote", "create", "company", j15.url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "company/"+j15.bundleName); err != nil {
		return err
	}
	j15.referenced = true
	return nil
}

// j15EnsureReferenced wires the reference if needed, then ALWAYS pulls — the
// only way to fetch whatever is currently at the remote's HEAD (including a
// commit added after the reference was first wired, e.g. Mallory's tamper or
// the company shipping an MCP server/hook onto an already-referenced bundle).
func j15EnsureReferenced(w *World) error {
	if err := j15WireReference(w); err != nil {
		return err
	}
	return runOK(w, "remote", "pull")
}

// j15StartSession is "Alice starts a session": ensure the company bundle is
// referenced and pulled, then materialize the default profile into "out" —
// the single command that writes CLAUDE.md AND the generated settings files
// (.mcp.json, .claude/settings.json) together (operations.MaterializeProfile),
// so scenarios 1/3/4 share one implementation. Exit code is ignored here (a
// strictness abort on a fatal trust finding is itself asserted by dedicated
// Then steps, mirroring steps_j2_setup.go's materializeDefault).
func j15StartSession(w *World) error {
	if err := j15EnsureReferenced(w); err != nil {
		return err
	}
	_ = w.env.Run("profile", "materialize", "default", "--target", "out")
	return nil
}

// j15ReadMaterialized reads out/CLAUDE.md, the assembled content surface.
func j15ReadMaterialized(w *World) (string, error) {
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

// j15AssertGeneratedSettingsContains/DoesNotContain check the GENERATED
// executable-surface files under the materialize target — the delivery path
// distinct from content assembly, and the whole point of scenarios 3-4 (task
// brief: "assert the actual GENERATED settings file").
func j15AssertMCPPresence(w *World, present bool) error {
	return j15AssertGeneratedFile(w, filepath.Join("out", ".mcp.json"), j15MCPMarker, present)
}

func j15AssertHookPresence(w *World, present bool) error {
	return j15AssertGeneratedFile(w, filepath.Join("out", ".claude", "settings.json"), j15HookMarker, present)
}

func j15AssertGeneratedFile(w *World, rel, marker string, present bool) error {
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

func registerJ15Steps(ctx *godog.ScenarioContext) {
	// --- Background ----------------------------------------------------------

	ctx.Step(`^Trent's company publishes a "([^"]*)" bundle, signed with the company key$`, func(c context.Context, bundleName string) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		j15 := j15Of(w)
		j15.bundleName = bundleName
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate company signer: %w", err)
		}
		j15.signer = signer
		rel := ".ctxloom/content/bundles/" + bundleName + ".yaml"
		url, err := w.env.SeedSignedRemote(map[string]string{rel: j15BundleYAML(j15CompanyMarker)}, []string{rel}, signer)
		if err != nil {
			return fmt.Errorf("seed signed company remote: %w", err)
		}
		j15.url = url
		j15.bare = strings.TrimPrefix(url, "file://")
		return nil
	})

	ctx.Step(`^Alice trusts the company key$`, func(c context.Context) error {
		w := worldFrom(c)
		j15 := j15Of(w)
		j15.principal = "trent@example.com"
		pubBytes := ssh.MarshalAuthorizedKey(j15.signer.Public)
		keyPath := filepath.Join(w.env.Root, "company-signer.pub")
		if err := os.WriteFile(keyPath, pubBytes, 0o644); err != nil {
			return fmt.Errorf("write company public key: %w", err)
		}
		// Drives the real "ctxloom trust signer create" leaf (project store, so the
		// whole team inherits the trust decision) — see completeness_test.go's
		// knownUncoveredCLI, pruned for this ref by J15.
		return runOK(w, "trust", "signer", "create", j15.principal, "--key", keyPath, "--project")
	})

	// --- Scenario 1: reference mechanic ---------------------------------------

	ctx.Step(`^Alice references the company's secure-coding bundle from her project$`, func(c context.Context) error {
		return j15EnsureReferenced(worldFrom(c))
	})

	// "Alice starts a session" is already registered by steps_j2_setup.go
	// (materialize "default" into "out") — reused as-is rather than duplicated
	// (godog rejects an ambiguous second match for the same step text). Every
	// J15 scenario that reaches this step has already wired+pulled the company
	// bundle in its own preceding Given/When step (j15EnsureReferenced), so
	// nothing J15-specific needs to happen here.

	ctx.Step(`^her assistant receives the company's secure-coding guidance, because the company key signed it$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j15ReadMaterialized(w)
		if err != nil {
			return err
		}
		if !strings.Contains(body, j15CompanyMarker) {
			return fmt.Errorf("materialized context does not contain the company's guidance marker; content:\n%s", body)
		}
		return nil
	})

	// --- Scenario 2: TAMPER ----------------------------------------------------

	ctx.Step(`^Mallory alters the company's secure-coding bundle after it was signed$`, func(c context.Context) error {
		w := worldFrom(c)
		j15 := j15Of(w)
		rel := ".ctxloom/content/bundles/" + j15.bundleName + ".yaml"
		// AdvanceRemote (not AdvanceSignedRemote): the bytes change but the OLD
		// ".sig" sibling — signed over the ORIGINAL content — survives
		// untouched, so it no longer verifies over these new bytes. That
		// mismatch IS signing.ErrSignatureTampered.
		if err := w.env.AdvanceRemote(j15.bare, map[string]string{rel: j15BundleYAML(j15TamperedMarker)}); err != nil {
			return fmt.Errorf("advance remote with tampered content: %w", err)
		}
		// Wire (but do not pull) the reference: scenario 2 has no antecedent
		// "Alice references..." step, so her first-ever sync below pulls
		// straight into the already-tampered HEAD.
		return j15WireReference(w)
	})

	ctx.Step(`^Alice syncs her project$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j15EnsureReferenced(w); err != nil {
			return err
		}
		_ = w.env.Run("profile", "materialize", "default", "--target", "out")
		return nil
	})

	ctx.Step(`^her assistant does not receive the altered guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j15ReadMaterialized(w)
		if err != nil {
			return err
		}
		if strings.Contains(body, j15TamperedMarker) {
			return fmt.Errorf("materialized context unexpectedly contains the tampered marker; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Alice is warned that the content's signature does not verify$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		// "Alice syncs her project" (the preceding When) is the step that
		// actually ran the materialize whose output this checks, so it — not
		// this Then — got the automatic CLIOutput attribution.
		w.docStepMaterialized = strings.TrimSpace(out)
		// The warning has to say the SIGNATURE is the problem, and it has to
		// name the item — "something was withheld" is not a diagnosis. The
		// wording is the trust filter's single rendering of the tampered
		// verdict (bundles.Reason.Explain), so this is the one place the
		// user-facing sentence for the §10.2 downgrade is asserted end to end.
		if !strings.Contains(out, "signature does not cover these bytes") {
			return fmt.Errorf("materialize output does not warn that the signature does not cover the content's bytes; output:\n%s", out)
		}
		if !strings.Contains(out, "#fragments/guidance") {
			return fmt.Errorf("materialize output does not name the withheld item; output:\n%s", out)
		}
		return nil
	})

	// --- Scenarios 3 & 4: EXECUTABLE trust gate --------------------------------

	ctx.Step(`^the company's bundle ships an MCP server and a hook$`, func(c context.Context) error {
		w := worldFrom(c)
		j15 := j15Of(w)
		rel := ".ctxloom/content/bundles/" + j15.bundleName + ".yaml"
		if err := w.env.AdvanceSignedRemote(j15.bare, map[string]string{rel: j15BundleYAMLWithExec(j15CompanyMarker)}, []string{rel}, j15.signer); err != nil {
			return fmt.Errorf("advance remote with mcp+hook: %w", err)
		}
		return j15EnsureReferenced(w)
	})

	ctx.Step(`^Alice has rejected the hook$`, func(c context.Context) error {
		w := worldFrom(c)
		j15 := j15Of(w)
		ref := j15.url + "@bundles/" + j15.bundleName + "#hooks/session_start/0"
		return runOK(w, "trust", "reject", ref)
	})

	ctx.Step(`^the MCP server appears in her assistant's configuration$`, func(c context.Context) error {
		return j15AssertMCPPresence(worldFrom(c), true)
	})

	ctx.Step(`^the hook appears in her assistant's configuration$`, func(c context.Context) error {
		return j15AssertHookPresence(worldFrom(c), true)
	})

	ctx.Step(`^the MCP server still appears in her configuration$`, func(c context.Context) error {
		return j15AssertMCPPresence(worldFrom(c), true)
	})

	ctx.Step(`^the hook is absent, because she rejected it$`, func(c context.Context) error {
		return j15AssertHookPresence(worldFrom(c), false)
	})

	// --- Scenario 5: RETRACTION — @wip, see the feature file's comment --------
	// (internal/remote/retract.go's CheckRetracted is only ever consulted by
	// Puller.confirmRetraction, and operations.syncItem — the only caller —
	// either skips already-installed refs before Pull ever runs, or hardcodes
	// Force:true when it does. EffectiveTrust never consults retraction at
	// all. Retraction currently has NO effect on already-distributed content
	// through any CLI path — see this journey's final report.)

	ctx.Step(`^Alice already receives the company's secure-coding guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j15StartSession(w); err != nil {
			return err
		}
		body, err := j15ReadMaterialized(w)
		if err != nil {
			return err
		}
		if !strings.Contains(body, j15CompanyMarker) {
			return fmt.Errorf("setup failed: Alice does not yet receive the company's guidance; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Trent retracts that version of the bundle$`, func(c context.Context) error {
		w := worldFrom(c)
		j15 := j15Of(w)
		manifest := fmt.Sprintf(
			"version: 1\nretracted:\n  - type: bundle\n    name: %q\n    version: \"\"\n    reason: %q\n",
			j15.bundleName, "found to be incorrect guidance; do not use")
		return w.env.AdvanceRemote(j15.bare, map[string]string{".ctxloom/content/manifest.yaml": manifest})
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
		body, err := j15ReadMaterialized(w)
		if err != nil {
			return err
		}
		if strings.Contains(body, j15CompanyMarker) {
			return fmt.Errorf("materialized context still contains the retracted guidance; content:\n%s", body)
		}
		return nil
	})

	// --- Scenario 6: KEY REVOCATION --------------------------------------------

	ctx.Step(`^Alice receives several bundles the company signed with its key$`, func(c context.Context) error {
		w := worldFrom(c)
		j15 := j15Of(w)
		if err := j15EnsureReferenced(w); err != nil { // the background's "secure-coding" bundle
			return err
		}
		for _, extra := range []struct{ name, marker string }{
			{"extra-a", j15ExtraMarkerA},
			{"extra-b", j15ExtraMarkerB},
		} {
			rel := ".ctxloom/content/bundles/" + extra.name + ".yaml"
			url, err := w.env.SeedSignedRemote(map[string]string{rel: j15BundleYAML(extra.marker)}, []string{rel}, j15.signer)
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
			j15.extraMarkers = append(j15.extraMarkers, extra.marker)
		}
		return runOK(w, "remote", "pull")
	})

	ctx.Step(`^the company key is compromised$`, func(c context.Context) error {
		// Narrative beat only — the consequence is what the next two steps
		// (revoking trust, then syncing) actually drive and assert.
		return nil
	})

	ctx.Step(`^Alice revokes her trust in the company key$`, func(c context.Context) error {
		w := worldFrom(c)
		// Drives the real "ctxloom trust signer delete" leaf — see
		// completeness_test.go's knownUncoveredCLI, pruned for this ref by J15.
		return runOK(w, "trust", "signer", "delete", j15Of(w).principal, "--project")
	})

	ctx.Step(`^her assistant no longer receives any content signed by that key$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j15ReadMaterialized(w)
		if err != nil {
			return err
		}
		markers := append([]string{j15CompanyMarker}, j15Of(w).extraMarkers...)
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
		out := w.env.LastOutput()
		for _, want := range []string{j15Of(w).bundleName, "guidance", "new"} {
			if !strings.Contains(out, want) {
				return fmt.Errorf("review --list does not show the formerly-signed content as pending %q; output:\n%s", want, out)
			}
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
		rel := ".ctxloom/content/bundles/bystander.yaml"
		url, err := w.env.SeedRemote(map[string]string{rel: j15BundleYAML(j15ForgeryMarker)})
		if err != nil {
			return fmt.Errorf("seed unsigned bystander remote: %w", err)
		}
		if err := runOK(w, "remote", "create", "bystander", url, "--forge", "git"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", "bystander/bystander"); err != nil {
			return err
		}
		return runOK(w, "remote", "pull")
	})

	ctx.Step(`^Alice tries to record a review decision into the team's shared store$`, func(c context.Context) error {
		w := worldFrom(c)
		// `ctxloom review` (sans --list) only reaches resolveReviewSigner on a
		// REAL tty (isInteractiveTerminal()) — a plain piped Run/RunWithStdin
		// would take the non-interactive list branch and never exercise this
		// path at all, mirroring steps_j2_common.go's driveDiscoverySessionViaMock.
		sess, err := w.env.RunPTY(100, 30, "review", "--project")
		if err != nil {
			return fmt.Errorf("start 'ctxloom review --project' pty: %w", err)
		}
		defer sess.Close()
		exited, waitErr := sess.Wait(10 * time.Second)
		if !exited {
			return fmt.Errorf("'ctxloom review --project' did not exit within timeout; captured output:\n%s", sess.Output())
		}
		_ = waitErr // the exit code (checked below) is the authoritative signal
		j15 := j15Of(w)
		j15.reviewPTYOutput = sess.Output()
		j15.reviewPTYExit = sess.ExitCode()
		// The refusal ("no signing key available") is PTY output, invisible to
		// the @doc sidecar's w.env view — surface it as this step's evidence.
		w.docStepMaterialized = j15.reviewPTYOutput
		return nil
	})

	ctx.Step(`^ctxloom refuses, because a team decision must be signed$`, func(c context.Context) error {
		w := worldFrom(c)
		j15 := j15Of(w)
		// j15.reviewPTYOutput was already attached to "Alice tries to record a
		// review decision..." via the set-and-consume docStepMaterialized
		// field, which that step already spent — this Then re-checks the same
		// PTY transcript, so re-attach it (plus the exit code the assertion is
		// actually keyed on) rather than leave this step's pane empty.
		w.docStepMaterialized = fmt.Sprintf("'ctxloom review --project' exit=%d:\n%s", j15.reviewPTYExit, strings.TrimSpace(j15.reviewPTYOutput))
		if j15.reviewPTYExit == 0 {
			return fmt.Errorf("expected 'ctxloom review --project' to refuse (non-zero exit) with no signing key available, got exit 0; output:\n%s", j15.reviewPTYOutput)
		}
		if !strings.Contains(j15.reviewPTYOutput, "no signing key available") {
			return fmt.Errorf("output does not explain the missing signing key; output:\n%s", j15.reviewPTYOutput)
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
