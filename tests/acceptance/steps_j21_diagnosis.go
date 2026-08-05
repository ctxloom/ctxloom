//go:build acceptance

// J21: "the day the assistant goes blind" (j21_diagnosis.feature) — the
// DIAGNOSIS walk. FLOWS-UNIFIED.md's U5, numbered J21 here because that
// document's "proposed J20" slot was taken by bundle distribution before this
// was written.
//
// THE SPINE IS THE BOUNDARY TABLE (FLOWS-UNIFIED.md Appendix A.2). Content
// travels authored → packaged → attested → distributed → admitted → composed →
// delivered → ingested, and the product's bar is that EVERY hop has an
// inspector that NAMES THE CAUSE when content stops arriving. One scenario per
// boundary: plant the cause, run the inspector, assert the inspector says the
// thing. A boundary with no inspector is a defect, and its scenario is red and
// stays red — B2 (edited-not-re-signed) and B7 (delivered-vs-ingested) are
// exactly those, and they are the reason this journey is worth wiring at all.
//
// WHY THE ASSERTIONS ARE SHAPED THE WAY THEY ARE. Every Then here reads a
// PAYLOAD: the bytes of the assembled context, or the inspector's own words
// naming a specific bundle/fragment/file. None of them asserts an exit code.
// This journey exists because a user could not tell WHICH hop dropped her
// content; an inspector that exits 0 while naming nothing is precisely the
// failure mode under test, so "the command succeeds" would be an assertion of
// the bug.
//
// SEAM WITH J18/J3. J18 owns the PRODUCTION of signatures and J3 owns the
// ADVERSARY. This journey owns neither: it re-uses signing only as a way to
// PLANT a cause, and every assertion is about what an INSPECTOR reports.
// Nothing here re-proves that a tampered bundle is detected, that a retraction
// propagates, or that `bundle sign` writes bytes.
//
// ISOLATION follows steps_j18_signing.go exactly: everything runs through
// testenv.TestEnvironment's isolated HOME/XDG, and the single environment
// variable this file sets is SSH_AUTH_SOCK, pointed at a hermetic in-process
// agent minted under w.env.Root. See that file's header for why this coexists
// with steps_j3.go's deliberate blanking of the same key.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// j21DeployMarker IS the deploy guidance: a distinctive string, so every
	// delivery assertion checks for exactly the right bytes rather than for a
	// file's existence or a command's exit code.
	j21DeployMarker = "J21-DEPLOY-PROCESS-MARKER"
	// j21RevisedMarker is Carol's Friday edit — the content that SHOULD be
	// arriving on Monday and is not.
	j21RevisedMarker = "J21-DEPLOY-PROCESS-REVISED-MARKER"

	// j21Bundle is the team's runbook bundle, and j21Fragment the item inside
	// it that carries the deploy process.
	j21Bundle   = "deploy-runbook"
	j21Fragment = "deploy-process"

	// j21KeyComment / j21Principal mirror J18's split: the ssh-agent comment a
	// human recognizes is deliberately NOT the allowed_signers principal, so a
	// fixture can never confuse the two.
	j21KeyComment = "carol@acme.example"
	j21Principal  = "runbooks@acme.example"
	j21PubKeyFile = "acme-runbooks.pub"

	// j21BundlesDir is the authored (committed, publishable) bundle tree.
	j21BundlesDir = ".ctxloom/content/bundles"
)

// j21State is this journey's fixture state.
type j21State struct {
	signer    *testenv.TestSigner
	stopAgent func() error
	ready     bool

	// Remote bookkeeping for the boundaries that need a real publish hop.
	bare       string
	url        string
	referenced bool

	// probes records every invocation an "Alice asks ctxloom ..." step tried
	// while looking for an inspector that does not exist, so the failure
	// message can name exactly what was probed rather than asserting against
	// one guessed spelling. See j21Probe.
	probes []j21ProbeResult
}

// j21ProbeResult is one candidate inspector invocation and what it produced.
type j21ProbeResult struct {
	args   []string
	exit   int
	output string
}

func j21Of(w *World) *j21State {
	if w.j21 == nil {
		w.j21 = &j21State{}
	}
	return w.j21
}

// j21BundleYAML renders the runbook bundle at a given version carrying content
// as the deploy process. Ordered, literal YAML for the same reason J18 does it:
// these exact bytes are what gets signed.
//
// The VERSION is a parameter because it is what makes a republish an actual
// PIN ADVANCE. Measured while writing this journey: editing a bundle's content
// while leaving `version:` alone republishes bytes that `remote update` never
// advances to, so the consumer keeps receiving the previous copy and the whole
// attestation boundary is never reached. B2's silent-loss mode is explicitly
// "withheld silently ON PIN ADVANCE", so the fixture has to produce one.
func j21BundleYAML(version, content string) string {
	return fmt.Sprintf("version: %q\nfragments:\n  %s:\n    content: %q\n", version, j21Fragment, content)
}

func j21BundlePath() string { return j21BundlesDir + "/" + j21Bundle + ".yaml" }

// j21Setup is the Background: a hermetic project with one engine, a seed
// bundle and a "default" profile, plus Carol's signing key live in an
// in-process ssh-agent so the attestation boundaries can plant their causes
// through the real `bundle sign` path.
func j21Setup(w *World) error {
	st := j21Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(w.env.ProjectDir, filepath.FromSlash(j21BundlesDir)), 0o755); err != nil {
		return fmt.Errorf("create authored bundles dir: %w", err)
	}

	signer, err := testenv.GenerateTestSigner()
	if err != nil {
		return fmt.Errorf("generate Carol's key: %w", err)
	}
	st.signer = signer
	if err := w.env.WriteFile(j21PubKeyFile, signer.AuthorizedKey(j21KeyComment)); err != nil {
		return fmt.Errorf("write Carol's public key: %w", err)
	}
	sock, stop, err := testenv.StartSSHAgent(w.env.Root, testenv.SSHAgentIdentity{Signer: signer, Comment: j21KeyComment})
	if err != nil {
		return fmt.Errorf("start hermetic ssh-agent: %w", err)
	}
	st.stopAgent = stop
	w.env.SetEnv("SSH_AUTH_SOCK", sock)
	if err := w.env.GitConfigLocal("user.signingkey", filepath.Join(w.env.ProjectDir, j21PubKeyFile)); err != nil {
		return err
	}

	st.ready = true
	return nil
}

// j21WriteAuthored writes the runbook into the authored tree at version
// 1.0.0 — Friday's copy, and the one every locally-authored scenario uses.
func j21WriteAuthored(w *World, content string) error {
	return w.env.WriteFile(j21BundlePath(), j21BundleYAML("1.0.0", content))
}

// j21WriteRevised writes Carol's revision at a HIGHER version, so republishing
// it is a genuine pin advance rather than a byte change the consumer's lock
// never moves to. See j21BundleYAML.
func j21WriteRevised(w *World, content string) error {
	return w.env.WriteFile(j21BundlePath(), j21BundleYAML("1.1.0", content))
}

// j21LockedName is the name the runbook carries in the active lockfile once it
// has been pulled from the team remote — remote-qualified, which is what
// `bundle hold` resolves against.
func j21LockedName() string { return "team/" + j21Bundle }

// j21PublishFromDisk publishes exactly what is on disk — bundle bytes plus
// whatever `.sig` sibling is (or is not) beside them — into the team remote,
// then hands the authoring copy off out of the project.
//
// The hand-off is load-bearing, for the reason steps_j18_signing.go's
// j18SeedFromDisk documents: one hermetic project plays both the publishing
// and the consuming checkout, and a LOCAL authored bundle of the same name
// shadows the remote one. Left in place, every "withheld" assertion in this
// journey would silently measure the local copy and pass while proving
// nothing.
func j21PublishFromDisk(w *World) error {
	st := j21Of(w)
	rel := j21BundlePath()
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read authored runbook to publish: %w", err)
	}
	files := map[string]string{rel: body}
	// The signature is published only if one exists — "published without a
	// signature" is a cause this journey deliberately plants.
	if sig, serr := w.env.ReadFile(rel + ".sig"); serr == nil {
		files[rel+".sig"] = sig
	}

	for _, p := range []string{rel, rel + ".sig"} {
		full := filepath.Join(w.env.ProjectDir, filepath.FromSlash(p))
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("hand off authored %s: %w", p, err)
		}
	}

	if st.bare == "" {
		url, serr := w.env.SeedRemote(files)
		if serr != nil {
			return fmt.Errorf("seed team remote: %w", serr)
		}
		st.url = url
		st.bare = strings.TrimPrefix(url, "file://")
		return nil
	}
	return w.env.AdvanceRemote(st.bare, files)
}

// j21Reference wires the team remote into the consuming project the ordinary
// way — remote create (an address, never trust), reference it from the composed
// profile, pull — and is idempotent so a scenario can pull again after the
// publisher advances.
func j21Reference(w *World) error {
	st := j21Of(w)
	if st.referenced {
		return runOK(w, "remote", "pull")
	}
	if st.url == "" {
		return fmt.Errorf("nothing has been published to the team remote yet")
	}
	if err := runOK(w, "remote", "create", "team", st.url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "team/"+j21Bundle); err != nil {
		return err
	}
	st.referenced = true
	return runOK(w, "remote", "pull")
}

// j21TrustCarol writes Carol's key into the committable project trust store,
// so the ATTESTATION boundary is the only thing left to plant a cause at.
func j21TrustCarol(w *World) error {
	return w.env.TrustSigner(j21Of(w).signer, j21Principal, true)
}

// j21Delivered materializes the default profile and returns the assembled
// context — the payload every delivery assertion in this journey reads.
func j21Delivered(w *World) (string, error) {
	body, err := materializeDefault(w, "out")
	if err != nil {
		return "", err
	}
	w.docStepMaterialized = body
	return body, nil
}

// j21AssertDelivery checks a marker's presence in the assembled context, and
// on failure prints the whole delivered payload — the only way to tell "the
// guidance was withheld" apart from "nothing was assembled at all", which is
// this codebase's characteristic bug.
func j21AssertDelivery(w *World, marker string, want bool) error {
	body, err := j21Delivered(w)
	if err != nil {
		return err
	}
	has := strings.Contains(body, marker)
	if want && !has {
		return fmt.Errorf("the assembled context does not carry the deploy guidance %q; delivered %d bytes:\n%s", marker, len(body), body)
	}
	if !want && has {
		return fmt.Errorf("the assembled context still carries %q, which this scenario planted a cause to withhold; delivered:\n%s", marker, body)
	}
	return nil
}

// j21Probe runs one candidate inspector invocation and records it. It never
// fails on a non-zero exit: a probe for a surface that does not exist is
// EXPECTED to fail, and the scenario's Then is what turns "no inspector
// answered" into a message naming every spelling that was tried.
func j21Probe(w *World, args ...string) {
	st := j21Of(w)
	_ = w.env.Run(args...)
	st.probes = append(st.probes, j21ProbeResult{
		args:   append([]string(nil), args...),
		exit:   w.env.LastExitCode(),
		output: w.env.LastOutput(),
	})
}

// j21ProbesAnswered reports whether ANY probed invocation produced output
// containing every one of wants. The failure message enumerates each probe
// with its exit code, so the red scenario documents exactly which surfaces
// were asked and what they said instead.
func j21ProbesAnswered(w *World, wants ...string) error {
	st := j21Of(w)
	if len(st.probes) == 0 {
		return fmt.Errorf("no inspector was probed — the step that asks the question did not run")
	}
	for _, p := range st.probes {
		all := true
		for _, want := range wants {
			if !strings.Contains(p.output, want) {
				all = false
				break
			}
		}
		if all {
			return nil
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "no ctxloom surface answered the question: none of the %d probed invocations reported all of %v.\n",
		len(st.probes), wants)
	for _, p := range st.probes {
		fmt.Fprintf(&b, "\n  $ ctxloom %s   (exit %d)\n%s\n", strings.Join(p.args, " "), p.exit, indentBlock(p.output))
	}
	return fmt.Errorf("%s", b.String())
}

// indentBlock indents a captured output block so a multi-probe failure message
// stays readable in godog's report.
func indentBlock(s string) string {
	if strings.TrimSpace(s) == "" {
		return "    (no output)"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// j21OutputNamesAll asserts the LAST command's output names every one of
// wants — the shape of "the inspector named the cause".
func j21OutputNamesAll(w *World, what string, wants ...string) error {
	out := w.env.LastOutput()
	var missing []string
	for _, want := range wants {
		if !strings.Contains(out, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s did not name %v; the whole output was:\n%s", what, missing, out)
	}
	return nil
}

func registerJ21Steps(ctx *godog.ScenarioContext) {
	// The in-process ssh-agent is this journey's only long-lived resource;
	// stopping it here joins its accept loop before the scenario is over.
	ctx.After(func(c context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		w := worldFrom(c)
		if w == nil || w.j21 == nil || w.j21.stopAgent == nil {
			return c, nil
		}
		stop := w.j21.stopAgent
		w.j21.stopAgent = nil
		if serr := stop(); serr != nil {
			return c, fmt.Errorf("stop hermetic ssh-agent: %w", serr)
		}
		return c, nil
	})

	// --- Background ---------------------------------------------------------

	ctx.Step(`^Alice's team ships its deploy process as ctxloom content$`, func(c context.Context) error {
		return j21Setup(worldFrom(c))
	})

	// --- B1: authored -> packaged -------------------------------------------

	ctx.Step(`^the deploy process exists only as a loose file in Alice's repo, in no bundle$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21Setup(w); err != nil {
			return err
		}
		// Authored, but never packaged: the text is real and on disk, and no
		// bundle references it. This is B1's silent-loss mode exactly.
		return w.env.WriteFile("docs/deploy-process.md", "# Deploy process\n\n"+j21DeployMarker+"\n")
	})

	ctx.Step(`^the search results name no packaged item carrying the deploy process$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if strings.Contains(out, j21Fragment) || strings.Contains(out, j21Bundle) {
			return fmt.Errorf("search named a packaged deploy item, but nothing was ever packaged; output:\n%s", out)
		}
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("search printed NOTHING at all — an inspector that answers a question with zero bytes cannot tell " +
				"'no such item' apart from 'the search never ran'; that silence is the defect this boundary exists to catch")
		}
		return nil
	})

	// --- B2: packaged -> attested -------------------------------------------

	ctx.Step(`^Carol published the signed runbook, and Alice's assistant receives its deploy guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21Setup(w); err != nil {
			return err
		}
		if err := j21WriteAuthored(w, j21DeployMarker); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "sign", j21Bundle); err != nil {
			return err
		}
		if err := j21TrustCarol(w); err != nil {
			return err
		}
		if err := j21PublishFromDisk(w); err != nil {
			return err
		}
		if err := j21Reference(w); err != nil {
			return err
		}
		return j21AssertDelivery(w, j21DeployMarker, true)
	})

	ctx.Step(`^Carol edits the runbook on Friday and never re-signs it$`, func(c context.Context) error {
		w := worldFrom(c)
		// The publisher's edit lands in the remote WITHOUT a refreshed
		// signature: the stale .sig is carried forward verbatim, which is what
		// really happens when someone edits a bundle and pushes. The version
		// moves, so this is a real pin advance — B2's stated trigger.
		if err := j21WriteRevised(w, j21RevisedMarker); err != nil {
			return err
		}
		return j21PublishFromDisk(w)
	})

	ctx.Step(`^Alice syncs on Monday$`, func(c context.Context) error {
		w := worldFrom(c)
		// An ordinary sync: no incident ceremony, no special flag. `remote
		// update` is allowed to report problems, so its exit code is not the
		// assertion — what it SAYS and what arrives afterwards are.
		//
		// `remote upgrade` is the third call and it is LOAD-BEARING. B2's
		// silent-loss mode is "withheld silently ON PIN ADVANCE", so a sync
		// that never advances a pin never reaches the attestation boundary at
		// all.
		//
		// MEASURED 2026-08-05, and the reason this line exists: bare `remote
		// update` is a DRY CHECK that ends in "Run with --apply to update all
		// items", and `remote pull` answers "Skipped (kept at their locked
		// commit): 1 — Pull never moves an existing pin. Run 'ctxloom remote
		// upgrade' to advance them." With only those two the lockfile stayed
		// at Friday's commit, so every "the revision never arrived" assertion
		// in this journey passed because NOTHING WAS EVER SYNCED — not because
		// anything was withheld. Two independent checks confirmed it: making
		// the fixture re-sign properly changed no outcome, and gutting
		// signing.VerifyPublisher so an invalid signature verifies changed no
		// outcome either. The signature gate was never consulted.
		//
		// `remote upgrade` is what the product's own pull output tells the user
		// to run, so it is the ordinary sync a person actually performs when
		// they want Monday's content.
		_ = w.env.Run("remote", "update")
		_ = w.env.Run("remote", "pull")
		_ = w.env.Run("remote", "upgrade")
		return nil
	})

	ctx.Step(`^her assistant never receives the revised deploy guidance$`, func(c context.Context) error {
		return j21AssertDelivery(worldFrom(c), j21RevisedMarker, false)
	})

	// MEASURED while writing this journey, and sharper than the symptom the
	// journey was written from. An unsigned republish is not merely dropped:
	// the consumer silently keeps serving the SUPERSEDED copy. The assistant
	// does not go quiet — it goes confidently stale, answering deploy questions
	// out of Friday's runbook with no indication anywhere that a newer one
	// exists and was refused. "It knew our deploy process Friday and doesn't
	// today" understates it; today it knows a process that is no longer true.
	ctx.Step(`^she is left silently serving the superseded copy$`, func(c context.Context) error {
		return j21AssertDelivery(worldFrom(c), j21DeployMarker, true)
	})

	// Reads LastStdout, not LastOutput, and that is the whole assertion.
	// LastOutput merges stderr, and the withhold advisory that rides stderr
	// here names the bundle by its full remote URL — a URL with
	// "deploy-runbook" in it. Against the merged stream this step tripped on
	// the WARNING while the pending list itself was printing exactly "Nothing
	// is pending review.", i.e. it reported the opposite of what the product
	// did. The claim is about what the LIST says, so it reads the list's own
	// stream. The empty-stdout guard below is what keeps that from becoming a
	// free pass.
	ctx.Step(`^the pending-review list does not name the runbook at all$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastStdout()
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("`review --list` printed NOTHING on stdout — an inspector that answers with zero bytes cannot be "+
				"read as saying the runbook is absent; it never said anything. The merged streams were:\n%s", w.env.LastOutput())
		}
		if strings.Contains(out, j21Bundle) {
			return fmt.Errorf("review --list DID name %q — if that is now true, this scenario has become stale and "+
				"the B2 inspector scenario below should be the one that passes; output:\n%s", j21Bundle, out)
		}
		return nil
	})

	ctx.Step(`^some inspector names the runbook as withheld because it is unsigned$`, func(c context.Context) error {
		return j21ProbesAnswered(worldFrom(c), j21Bundle, "unsigned")
	})

	ctx.Step(`^Alice asks ctxloom why the runbook stopped arriving$`, func(c context.Context) error {
		w := worldFrom(c)
		// Every inspector FLOWS-UNIFIED names for this hop, plus the two
		// plausible spellings a user would reach for. None of them is claimed
		// to exist; the assertion is that SOMETHING answers.
		j21Probe(w, "doctor")
		j21Probe(w, "review", "--list")
		j21Probe(w, "bundle", "list")
		j21Probe(w, "bundle", "show", j21LockedName())
		j21Probe(w, "trust", "signer", "list")
		return nil
	})

	// --- B3: attested -> distributed ----------------------------------------

	ctx.Step(`^Carol publishes a newer signed runbook while Alice's copy is held$`, func(c context.Context) error {
		w := worldFrom(c)
		// Held by its LOCKFILE name (remote-qualified): `bundle hold` resolves
		// against the active lockfile, and the bare name is not an entry there.
		if err := runOK(w, "bundle", "hold", j21LockedName()); err != nil {
			return err
		}
		if err := j21WriteRevised(w, j21RevisedMarker); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "sign", j21Bundle); err != nil {
			return err
		}
		return j21PublishFromDisk(w)
	})

	ctx.Step(`^the installed-bundle listing names the runbook as held$`, func(c context.Context) error {
		return j21OutputNamesAll(worldFrom(c), "the installed-bundle listing", j21Bundle, "held")
	})

	ctx.Step(`^her assistant still receives the older deploy guidance and not the newer$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21AssertDelivery(w, j21DeployMarker, true); err != nil {
			return err
		}
		return j21AssertDelivery(w, j21RevisedMarker, false)
	})

	// --- B4: distributed -> admitted ----------------------------------------

	ctx.Step(`^Carol published the signed runbook from a key Alice has never reviewed$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21Setup(w); err != nil {
			return err
		}
		if err := j21WriteAuthored(w, j21DeployMarker); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "sign", j21Bundle); err != nil {
			return err
		}
		// Deliberately NOT trusted: this is B4's cause.
		if err := j21PublishFromDisk(w); err != nil {
			return err
		}
		return j21Reference(w)
	})

	ctx.Step(`^the pending list names the deploy fragment of the runbook$`, func(c context.Context) error {
		return j21OutputNamesAll(worldFrom(c), "the pending-review list", j21Bundle, j21Fragment)
	})

	ctx.Step(`^the pending list says the runbook's signer is one she does not trust, naming the key$`, func(c context.Context) error {
		w := worldFrom(c)
		return j21OutputNamesAll(worldFrom(c), "the pending-review list", "untrusted", j21Of(w).signer.Fingerprint())
	})

	ctx.Step(`^her assistant does not receive the deploy guidance$`, func(c context.Context) error {
		return j21AssertDelivery(worldFrom(c), j21DeployMarker, false)
	})

	// --- B5: admitted -> composed -------------------------------------------

	ctx.Step(`^the runbook is installed and admitted, but composed into no profile$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21Setup(w); err != nil {
			return err
		}
		if err := j21WriteAuthored(w, j21DeployMarker); err != nil {
			return err
		}
		// Authored locally and first-party trusted on authorship alone: the
		// only thing missing is the composition step.
		return nil
	})

	ctx.Step(`^the profile listing does not name the runbook among its bundles$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("`profile show` printed nothing — it cannot be the inspector for this boundary if it renders no bundles at all")
		}
		if strings.Contains(out, j21Bundle) {
			return fmt.Errorf("the profile listing names %q, but this scenario composed it into no profile; output:\n%s", j21Bundle, out)
		}
		return nil
	})

	// STRENGTHENED 2026-08-05. This asserted only that "default" appeared
	// SOMEWHERE in `agent show default`'s output — and the agent in this
	// fixture is ITSELF named "default", so the assertion was satisfied by the
	// echoed argument and could not tell the agent's own name from the profile
	// it composes. Measured: deleting writeBulletList(w, "Profiles", …) from
	// renderAgentShow entirely left this step GREEN. It now reads the rendered
	// section — writeBulletList's "Profiles:" heading plus the "- default"
	// bullet under it — so that same deletion turns it red.
	ctx.Step(`^the agent listing names the profile it composes$`, func(c context.Context) error {
		return j21OutputNamesAll(worldFrom(c), "the agent listing", "Profiles:", "- default")
	})

	ctx.Step(`^the dry run shows her the deploy guidance that would be composed$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if !strings.Contains(out, j21DeployMarker) {
			return fmt.Errorf("`run --dry-run` did not show the composed context: the deploy guidance %q is nowhere in its output, "+
				"so it cannot answer 'is my content in the context this run would send?'. What it printed was:\n%s", j21DeployMarker, out)
		}
		return nil
	})

	ctx.Step(`^the runbook is composed into Alice's profile$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21Setup(w); err != nil {
			return err
		}
		if err := j21WriteAuthored(w, j21DeployMarker); err != nil {
			return err
		}
		return runOK(w, "profile", "modify", "default", "--add-bundle", j21Bundle)
	})

	// --- B6: composed -> delivered ------------------------------------------

	ctx.Step(`^the engine's own surface on disk still holds last week's copy$`, func(c context.Context) error {
		w := worldFrom(c)
		// Materialize once (so the surface exists and is genuinely ctxloom's),
		// then let the CONTENT move on underneath it without re-materializing.
		// That is exactly "stale materialization": the composed context and the
		// file the engine will actually read have diverged.
		if _, err := j21Delivered(w); err != nil {
			return err
		}
		if err := j21WriteAuthored(w, j21RevisedMarker); err != nil {
			return err
		}
		body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return fmt.Errorf("read the materialized surface: %w", err)
		}
		if !strings.Contains(body, j21DeployMarker) {
			return fmt.Errorf("the materialized surface does not hold last week's copy, so this scenario planted no staleness; it holds:\n%s", body)
		}
		if strings.Contains(body, j21RevisedMarker) {
			return fmt.Errorf("the materialized surface already carries this week's copy — nothing re-materialized it, so the fixture is wrong; it holds:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^the wiring report names the materialized surface as stale$`, func(c context.Context) error {
		return j21OutputNamesAll(worldFrom(c), "`manage status`", "stale")
	})

	// --- B7: delivered -> ingested ------------------------------------------

	ctx.Step(`^every earlier inspector is green and the deploy guidance is materialized into the engine's own surface$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21Setup(w); err != nil {
			return err
		}
		if err := j21WriteAuthored(w, j21DeployMarker); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", j21Bundle); err != nil {
			return err
		}
		return j21AssertDelivery(w, j21DeployMarker, true)
	})

	ctx.Step(`^Alice asks ctxloom what her engine actually ingested$`, func(c context.Context) error {
		w := worldFrom(c)
		// B7 has NO named inspector anywhere in the product (FLOWS-UNIFIED
		// Appendix A.2, verdict DEFECT), so there is no single spelling to
		// assert against. Probe every surface that could plausibly own the
		// answer; the Then reports all of them.
		j21Probe(w, "doctor")
		j21Probe(w, "manage", "status")
		j21Probe(w, "agent", "show", "default")
		j21Probe(w, "session", "list")
		return nil
	})

	ctx.Step(`^ctxloom names the surface her engine read and confirms the deploy guidance was in it$`, func(c context.Context) error {
		return j21ProbesAnswered(worldFrom(c), "CLAUDE.md", j21DeployMarker)
	})

	// --- M5: the two-machine symptom ----------------------------------------

	ctx.Step(`^Bob's checkout of the same project delivers the deploy guidance and Alice's does not$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j21Setup(w); err != nil {
			return err
		}
		if err := j21WriteAuthored(w, j21DeployMarker); err != nil {
			return err
		}
		// Bob's machine: the runbook composed into his profile, materialized,
		// and kept OUTSIDE Alice's project so the two are genuinely separate
		// deliveries rather than one file read twice.
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", j21Bundle); err != nil {
			return err
		}
		bobs, err := j21Delivered(w)
		if err != nil {
			return err
		}
		bobDir := filepath.Join(w.env.Root, "bob-delivered")
		if err := os.MkdirAll(bobDir, 0o755); err != nil {
			return fmt.Errorf("create Bob's delivered dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(bobDir, "CLAUDE.md"), []byte(bobs), 0o644); err != nil {
			return fmt.Errorf("record Bob's delivered context: %w", err)
		}
		// Alice's machine: the same project, the runbook removed from her
		// profile — the ordinary "it works on his machine" divergence.
		if err := runOK(w, "profile", "modify", "default", "--remove-bundle", j21Bundle); err != nil {
			return err
		}
		return j21AssertDelivery(w, j21DeployMarker, false)
	})

	ctx.Step(`^Alice asks ctxloom to compare her delivered context with Bob's$`, func(c context.Context) error {
		w := worldFrom(c)
		bobFile := filepath.Join(w.env.Root, "bob-delivered", "CLAUDE.md")
		// M5 (FLOWS-UNIFIED §4 finding class (b)): every diagnostic ctxloom has
		// is single-machine. Probe the surfaces that would own a comparison.
		j21Probe(w, "profile", "show", "default", "--compare", bobFile)
		j21Probe(w, "doctor", "--compare", bobFile)
		j21Probe(w, "profile", "materialize", "default", "--diff", bobFile)
		return nil
	})

	ctx.Step(`^ctxloom reports the deploy guidance as present for Bob and absent for her$`, func(c context.Context) error {
		return j21ProbesAnswered(worldFrom(c), j21DeployMarker)
	})
}
