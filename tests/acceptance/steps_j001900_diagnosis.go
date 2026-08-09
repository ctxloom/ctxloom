//go:build acceptance

// J001900: "the day the assistant goes blind" (j001900_diagnosis.feature) — the
// DIAGNOSIS walk. FLOWS-UNIFIED.md's U5, numbered J001900 here because that
// document's "proposed J001400" slot was taken by bundle distribution before this
// was written.
//
// THE SPINE IS THE BOUNDARY TABLE (FLOWS-UNIFIED.md Appendix A.2). Content
// travels authored → packaged → attested → distributed → admitted → composed →
// delivered → ingested, and the product's bar is that EVERY hop has an
// inspector that NAMES THE CAUSE when content stops arriving. One scenario per
// boundary: plant the cause, run the inspector, assert the inspector says the
// thing. A boundary with no inspector is a defect, and its scenario stays red
// until one exists — B6 (composed-vs-delivered staleness) and M5 (the
// two-machine symptom) are still that, each blocked on an architecture
// decision (hefty-gallery, obvious-pastime).
//
// B7 (delivered-vs-ingested) is a genuine exception, not a defect awaiting an
// inspector: whether the vendor engine actually READ the file ctxloom handed
// it happens inside a process ctxloom does not own, and no inspector will
// ever cross that boundary. Its scenario does not assert that impossible
// capability — inverted 2026-08-05 to assert instead that ctxloom SAYS it
// cannot know, which is testable and stays green. See that scenario's own
// comment in the .feature file for the decision and the mutation that backs
// it.
//
// WHY THE ASSERTIONS ARE SHAPED THE WAY THEY ARE. Every Then here reads a
// PAYLOAD: the bytes of the assembled context, or the inspector's own words
// naming a specific bundle/fragment/file. None of them asserts an exit code.
// This journey exists because a user could not tell WHICH hop dropped her
// content; an inspector that exits 0 while naming nothing is precisely the
// failure mode under test, so "the command succeeds" would be an assertion of
// the bug.
//
// SEAM WITH J001600/J001500. J001600 owns the PRODUCTION of signatures and J001500 owns the
// ADVERSARY. This journey owns neither: it re-uses signing only as a way to
// PLANT a cause, and every assertion is about what an INSPECTOR reports.
// Nothing here re-proves that a tampered bundle is detected, that a retraction
// propagates, or that `bundle sign` writes bytes.
//
// ISOLATION follows steps_j001600_signing.go exactly: everything runs through
// testenv.TestEnvironment's isolated HOME/XDG, and the single environment
// variable this file sets is SSH_AUTH_SOCK, pointed at a hermetic in-process
// agent minted under w.env.Root. See that file's header for why this coexists
// with steps_j001500.go's deliberate blanking of the same key.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// j001900DeployMarker IS the deploy guidance: a distinctive string, so every
	// delivery assertion checks for exactly the right bytes rather than for a
	// file's existence or a command's exit code.
	j001900DeployMarker = "J001900-DEPLOY-PROCESS-MARKER"
	// j001900RevisedMarker is Carol's Friday edit — the content that SHOULD be
	// arriving on Monday and is not.
	j001900RevisedMarker = "J001900-DEPLOY-PROCESS-REVISED-MARKER"

	// j001900Bundle is the team's runbook bundle, and j001900Fragment the item inside
	// it that carries the deploy process.
	j001900Bundle   = "deploy-runbook"
	j001900Fragment = "deploy-process"

	// j001900KeyComment / j001900Principal mirror J001600's split: the ssh-agent comment a
	// human recognizes is deliberately NOT the allowed_signers principal, so a
	// fixture can never confuse the two.
	j001900KeyComment = "carol@acme.example"
	j001900Principal  = "runbooks@acme.example"
	j001900PubKeyFile = "acme-runbooks.pub"

	// j001900BundlesDir is the authored (committed, publishable) bundle tree.
	j001900BundlesDir = ".ctxloom/content/bundles"
)

// j001900State is this journey's fixture state.
type j001900State struct {
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
	// one guessed spelling. See j001900Probe.
	probes []j001900ProbeResult

	// pinBeforeSync is the runbook's locked commit read off lock.yaml
	// IMMEDIATELY BEFORE Monday's sync runs. It is what makes "the pin did not
	// advance" a payload assertion rather than a re-reading of whatever the
	// lockfile happens to say afterwards: the scenario compares the file
	// against a value captured before the command that would have moved it.
	pinBeforeSync string

	// syncExit is the exit code Monday's `remote upgrade` returned, captured
	// alongside syncOutput and for the same reason: every later Then runs more
	// commands, so w.env.LastExitCode() has moved on by the time the code is
	// asserted. A script running an unattended sync reads only this — the
	// refusal message is for the human — so it is asserted separately from
	// what was printed.
	syncExit int

	// syncOutput is what Monday's `remote upgrade` said, captured at the
	// moment it ran. Every later Then in this journey materializes the profile
	// to read a payload, and materializing is itself a command — so
	// w.env.LastOutput() no longer holds the sync's words by the time a "she
	// was told" assertion runs. Capturing here is what keeps the message
	// assertion honest instead of order-dependent.
	syncOutput string
}

// j001900ProbeResult is one candidate inspector invocation and what it produced.
type j001900ProbeResult struct {
	args   []string
	exit   int
	output string
}

func j001900Of(w *World) *j001900State {
	if w.j001900 == nil {
		w.j001900 = &j001900State{}
	}
	return w.j001900
}

// j001900BundleYAML renders the runbook bundle at a given version carrying content
// as the deploy process. Ordered, literal YAML for the same reason J001600 does it:
// these exact bytes are what gets signed.
//
// The VERSION is a parameter because it is what makes a republish an actual
// PIN ADVANCE. Measured while writing this journey: editing a bundle's content
// while leaving `version:` alone republishes bytes that `remote update` never
// advances to, so the consumer keeps receiving the previous copy and the whole
// attestation boundary is never reached. B2's silent-loss mode is explicitly
// "withheld silently ON PIN ADVANCE", so the fixture has to produce one.
func j001900BundleYAML(version, content string) string {
	return fmt.Sprintf("version: %q\nfragments:\n  %s:\n    content: %q\n", version, j001900Fragment, content)
}

func j001900BundlePath() string { return j001900BundlesDir + "/" + j001900Bundle + ".yaml" }

// j001900Setup is the Background: a hermetic project with one engine, a seed
// bundle and a "default" profile, plus Carol's signing key live in an
// in-process ssh-agent so the attestation boundaries can plant their causes
// through the real `bundle sign` path.
func j001900Setup(w *World) error {
	st := j001900Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(w.env.ProjectDir, filepath.FromSlash(j001900BundlesDir)), 0o755); err != nil {
		return fmt.Errorf("create authored bundles dir: %w", err)
	}

	signer, err := testenv.GenerateTestSigner()
	if err != nil {
		return fmt.Errorf("generate Carol's key: %w", err)
	}
	st.signer = signer
	if err := w.env.WriteFile(j001900PubKeyFile, signer.AuthorizedKey(j001900KeyComment)); err != nil {
		return fmt.Errorf("write Carol's public key: %w", err)
	}
	sock, stop, err := testenv.StartSSHAgent(w.env.Root, testenv.SSHAgentIdentity{Signer: signer, Comment: j001900KeyComment})
	if err != nil {
		return fmt.Errorf("start hermetic ssh-agent: %w", err)
	}
	st.stopAgent = stop
	w.env.SetEnv("SSH_AUTH_SOCK", sock)
	if err := w.env.GitConfigLocal("user.signingkey", filepath.Join(w.env.ProjectDir, j001900PubKeyFile)); err != nil {
		return err
	}

	st.ready = true
	return nil
}

// j001900WriteAuthored writes the runbook into the authored tree at version
// 1.0.0 — Friday's copy, and the one every locally-authored scenario uses.
func j001900WriteAuthored(w *World, content string) error {
	return w.env.WriteFile(j001900BundlePath(), j001900BundleYAML("1.0.0", content))
}

// j001900WriteRevised writes Carol's revision at a HIGHER version, so republishing
// it is a genuine pin advance rather than a byte change the consumer's lock
// never moves to. See j001900BundleYAML.
func j001900WriteRevised(w *World, content string) error {
	return w.env.WriteFile(j001900BundlePath(), j001900BundleYAML("1.1.0", content))
}

// j001900LockedName is the name the runbook carries in the active lockfile once it
// has been pulled from the team remote — remote-qualified, which is what
// `bundle hold` resolves against.
func j001900LockedName() string { return "team/" + j001900Bundle }

// j001900PublishFromDisk publishes exactly what is on disk — bundle bytes plus
// whatever `.sig` sibling is (or is not) beside them — into the team remote,
// then hands the authoring copy off out of the project.
//
// The hand-off is load-bearing, for the reason steps_j001600_signing.go's
// j001600SeedFromDisk documents: one hermetic project plays both the publishing
// and the consuming checkout, and a LOCAL authored bundle of the same name
// shadows the remote one. Left in place, every "withheld" assertion in this
// journey would silently measure the local copy and pass while proving
// nothing.
func j001900PublishFromDisk(w *World) error {
	st := j001900Of(w)
	rel := j001900BundlePath()
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

// j001900Reference wires the team remote into the consuming project the ordinary
// way — remote create (an address, never trust), reference it from the composed
// profile, pull — and is idempotent so a scenario can pull again after the
// publisher advances.
func j001900Reference(w *World) error {
	st := j001900Of(w)
	if st.referenced {
		return runOK(w, "remote", "pull")
	}
	if st.url == "" {
		return fmt.Errorf("nothing has been published to the team remote yet")
	}
	if err := runOK(w, "remote", "create", "team", st.url, "--forge", "git"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "team/"+j001900Bundle); err != nil {
		return err
	}
	st.referenced = true
	return runOK(w, "remote", "pull")
}

// j001900TrustCarol writes Carol's key into the committable project trust store,
// so the ATTESTATION boundary is the only thing left to plant a cause at.
func j001900TrustCarol(w *World) error {
	return w.env.TrustSigner(j001900Of(w).signer, j001900Principal, true)
}

// j001900Delivered materializes the default profile and returns the assembled
// context — the payload every delivery assertion in this journey reads.
func j001900Delivered(w *World) (string, error) {
	body, err := materializeDefault(w, "out")
	if err != nil {
		return "", err
	}
	w.docStepMaterialized = body
	return body, nil
}

// j001900AssertDelivery checks a marker's presence in the assembled context, and
// on failure prints the whole delivered payload — the only way to tell "the
// guidance was withheld" apart from "nothing was assembled at all", which is
// this codebase's characteristic bug.
func j001900AssertDelivery(w *World, marker string, want bool) error {
	body, err := j001900Delivered(w)
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

// j001900Probe runs one candidate inspector invocation and records it. It never
// fails on a non-zero exit: a probe for a surface that does not exist is
// EXPECTED to fail, and the scenario's Then is what turns "no inspector
// answered" into a message naming every spelling that was tried.
func j001900Probe(w *World, args ...string) {
	st := j001900Of(w)
	_ = w.env.Run(args...)
	st.probes = append(st.probes, j001900ProbeResult{
		args:   append([]string(nil), args...),
		exit:   w.env.LastExitCode(),
		output: w.env.LastOutput(),
	})
}

// j001900ProbesAnswered reports whether ANY probed invocation produced output
// containing every one of wants. The failure message enumerates each probe
// with its exit code, so the red scenario documents exactly which surfaces
// were asked and what they said instead.
func j001900ProbesAnswered(w *World, wants ...string) error {
	st := j001900Of(w)
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

// j001900OutputNamesAll asserts the LAST command's output names every one of
// wants — the shape of "the inspector named the cause".
func j001900OutputNamesAll(w *World, what string, wants ...string) error {
	return j001900NamesAll(w.env.LastOutput(), what, wants...)
}

// j001900NamesAll is j001900OutputNamesAll over an already-captured output, for the
// assertions that must read what a specific EARLIER command said rather than
// whatever ran most recently.
func j001900NamesAll(out, what string, wants ...string) error {
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

// j001900LockedSHA reads the runbook's pinned commit straight out of the project's
// lock.yaml.
//
// It reads the FILE rather than asking a ctxloom command what is pinned, on
// purpose: the claim this journey makes at this boundary is about persisted
// state, and a command that renders the pin from the same code path that
// writes it could agree with itself while the file said something else.
func j001900LockedSHA(w *World) (string, error) {
	raw, err := w.env.ReadFile(filepath.Join(".ctxloom", "lock.yaml"))
	if err != nil {
		return "", fmt.Errorf("read lock.yaml: %w", err)
	}
	var lock struct {
		Bundles map[string]struct {
			SHA string `yaml:"sha"`
		} `yaml:"bundles"`
	}
	if err := yaml.Unmarshal([]byte(raw), &lock); err != nil {
		return "", fmt.Errorf("parse lock.yaml: %w\n%s", err, raw)
	}
	for key, entry := range lock.Bundles {
		if strings.Contains(key, j001900Bundle) {
			if entry.SHA == "" {
				return "", fmt.Errorf("lock.yaml pins %q with an EMPTY sha — an unpinned entry silently reads the branch tip:\n%s", key, raw)
			}
			return entry.SHA, nil
		}
	}
	return "", fmt.Errorf("lock.yaml has no entry for %q, so there is no pin to reason about:\n%s", j001900Bundle, raw)
}

func registerJ001900Steps(ctx *godog.ScenarioContext) {
	// The in-process ssh-agent is this journey's only long-lived resource;
	// stopping it here joins its accept loop before the scenario is over.
	ctx.After(func(c context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		w := worldFrom(c)
		if w == nil || w.j001900 == nil || w.j001900.stopAgent == nil {
			return c, nil
		}
		stop := w.j001900.stopAgent
		w.j001900.stopAgent = nil
		if serr := stop(); serr != nil {
			return c, fmt.Errorf("stop hermetic ssh-agent: %w", serr)
		}
		return c, nil
	})

	// --- Background ---------------------------------------------------------

	ctx.Step(`^Alice's team ships its deploy process as ctxloom content$`, func(c context.Context) error {
		return j001900Setup(worldFrom(c))
	})

	// --- B1: authored -> packaged -------------------------------------------

	ctx.Step(`^the deploy process exists only as a loose file in Alice's repo, in no bundle$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900Setup(w); err != nil {
			return err
		}
		// Authored, but never packaged: the text is real and on disk, and no
		// bundle references it. This is B1's silent-loss mode exactly.
		return w.env.WriteFile("docs/deploy-process.md", "# Deploy process\n\n"+j001900DeployMarker+"\n")
	})

	ctx.Step(`^the search results name no packaged item carrying the deploy process$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if strings.Contains(out, j001900Fragment) || strings.Contains(out, j001900Bundle) {
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
		if err := j001900Setup(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900DeployMarker); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "sign", j001900Bundle); err != nil {
			return err
		}
		if err := j001900TrustCarol(w); err != nil {
			return err
		}
		if err := j001900PublishFromDisk(w); err != nil {
			return err
		}
		if err := j001900Reference(w); err != nil {
			return err
		}
		return j001900AssertDelivery(w, j001900DeployMarker, true)
	})

	ctx.Step(`^Carol edits the runbook on Friday and never re-signs it$`, func(c context.Context) error {
		w := worldFrom(c)
		// The publisher's edit lands in the remote WITHOUT a refreshed
		// signature: the stale .sig is carried forward verbatim, which is what
		// really happens when someone edits a bundle and pushes. The version
		// moves, so this is a real pin advance — B2's stated trigger.
		if err := j001900WriteRevised(w, j001900RevisedMarker); err != nil {
			return err
		}
		return j001900PublishFromDisk(w)
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
		// Read the pin BEFORE the command that would move it, so the
		// "it did not advance" assertion compares against a captured value and
		// not against the lockfile explaining itself.
		pin, err := j001900LockedSHA(w)
		if err != nil {
			return err
		}
		j001900Of(w).pinBeforeSync = pin
		_ = w.env.Run("remote", "upgrade")
		j001900Of(w).syncOutput = w.env.LastOutput()
		j001900Of(w).syncExit = w.env.LastExitCode()
		return nil
	})

	ctx.Step(`^her assistant never receives the revised deploy guidance$`, func(c context.Context) error {
		return j001900AssertDelivery(worldFrom(c), j001900RevisedMarker, false)
	})

	// --- B2, the decided behaviour: refuse the advance ----------------------
	//
	// Three assertions, deliberately separate, because they are three
	// different ways this can be wrong: the pin moved anyway; the pin held but
	// the content stopped arriving; the pin held and the content arrived and
	// nobody was told, which is indistinguishable from "already up to date".

	ctx.Step(`^the runbook's pin did not advance, and the lockfile still holds the commit whose signature verified$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001900Of(w)
		if st.pinBeforeSync == "" {
			return fmt.Errorf("no pre-sync pin was captured — the sync step did not run, so 'it did not advance' would be vacuous")
		}
		after, err := j001900LockedSHA(w)
		if err != nil {
			return err
		}
		if after != st.pinBeforeSync {
			return fmt.Errorf("the pin ADVANCED onto content whose signature does not verify: lock.yaml went from %s to %s. "+
				"Upgrade must keep the last verified pin — advancing here withholds the new copy as tampered AND puts the old one "+
				"out of reach, leaving nothing", st.pinBeforeSync, after)
		}
		return nil
	})

	ctx.Step(`^her assistant is still served the content at that pin$`, func(c context.Context) error {
		// This is the whole reason option (a) was chosen: she goes on working.
		return j001900AssertDelivery(worldFrom(c), j001900DeployMarker, true)
	})

	ctx.Step(`^the sync told her the runbook cannot be verified, naming the pin it kept$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001900Of(w)
		out := st.syncOutput
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("the sync printed NOTHING — a silent non-advance is indistinguishable from 'everything is up to date', " +
				"which is the defect this row exists to prevent")
		}
		if strings.Contains(out, "Everything is up to date.") {
			return fmt.Errorf("the sync claimed everything is up to date while refusing an advance; it said:\n%s", out)
		}
		kept := st.pinBeforeSync
		if len(kept) > 16 {
			kept = kept[:16] // the pin is rendered abbreviated for humans
		}
		return j001900NamesAll(out, "Monday's sync", j001900Bundle, "does not verify", kept)
	})

	// The remedy has to be one that EXISTS. Before this row was written,
	// upgrade exited 0 saying "Changed content from untrusted sources is
	// withheld until reviewed: ctxloom review" — and `ctxloom review` answered
	// "Nothing is pending review.", because content withheld as tampered is
	// deliberately never offered for review. This step re-proves that dead end
	// is still a dead end, and then holds the sync's own words to it.
	ctx.Step(`^the remedy it named is not the review queue, which has nothing to offer her$`, func(c context.Context) error {
		w := worldFrom(c)
		out := j001900Of(w).syncOutput
		_ = w.env.Run("review", "--list")
		reviewSays := w.env.LastStdout()
		if !strings.Contains(reviewSays, "Nothing is pending review") {
			return fmt.Errorf("`review --list` no longer answers 'Nothing is pending review.' here — if tampered content became "+
				"reviewable, this row's premise changed and the assertion below is stale. It said:\n%s", reviewSays)
		}
		if strings.Contains(out, "ctxloom review") {
			return fmt.Errorf("the sync sent her to `ctxloom review`, which answers %q for this content — a remedy that cannot act "+
				"is worse than none. The sync said:\n%s", strings.TrimSpace(reviewSays), out)
		}
		if !strings.Contains(out, "re-sign") {
			return fmt.Errorf("the sync named no action she can actually take: nothing in it points at getting the content re-signed. "+
				"It said:\n%s", out)
		}
		return nil
	})

	// THE MACHINE-READABLE HALF. Every other Then on this row reads what a
	// HUMAN was told; this one reads what a SCRIPT was told, and they are
	// different audiences with different failure modes. An unattended sync that
	// refuses and exits 0 is indistinguishable, to the cron job that ran it,
	// from one that had nothing to do — the refusal message scrolls past into a
	// log nobody reads. Exit 2 (cli.exitCodeRefused, docs/cli-ux-principles.md
	// §7) is "completed, and deliberately did not do something asked": not 1,
	// which would report a decision as a fault on the user's machine, and not
	// 0, which is the silence.
	ctx.Step(`^the sync exited with the code for "did some of this deliberately not happen"$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001900Of(w)
		switch st.syncExit {
		case 2:
			return nil
		case 0:
			return fmt.Errorf("the sync exited 0 after refusing an advance — to a script that is indistinguishable from a round with "+
				"nothing to do, which is the whole silence this row exists to remove. It printed:\n%s", st.syncOutput)
		case 1:
			return fmt.Errorf("the sync exited 1 after refusing an advance. A refusal is not an error: nothing failed and the user's "+
				"environment is fine, and 1 sends them hunting a fault on their own machine. It printed:\n%s", st.syncOutput)
		default:
			return fmt.Errorf("the sync exited %d; the documented code for a deliberate non-action is 2 (docs/cli-ux-principles.md §7). "+
				"It printed:\n%s", st.syncExit, st.syncOutput)
		}
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
		if strings.Contains(out, j001900Bundle) {
			return fmt.Errorf("review --list DID name %q — if that is now true, this scenario has become stale and "+
				"the B2 inspector scenario below should be the one that passes; output:\n%s", j001900Bundle, out)
		}
		return nil
	})

	// THE ROW'S SUBJECT MOVED WITH THE PRODUCT, and the assertion moved with
	// it. It used to look for the word "unsigned", which was never true of the
	// cause this fixture plants: Carol's bundle IS signed — she edited the
	// bytes and carried the old .sig forward, which is a signature that does
	// not cover what it sits beside, a different state from unsigned entirely
	// (docs/trust-model.md, "Item states"). And since upgrade started REFUSING
	// that advance, nothing is withheld at all: the kept pin verifies, the
	// guidance keeps arriving, and only the REVISION is missing.
	//
	// So the question this row asks — "why is the newer copy not here, days
	// after the sync that refused it?" — is unchanged, and the three facts an
	// answer needs are: which bundle, that its signature does not verify, and
	// which pin is being served instead. Asserting all three is strictly more
	// than the two tokens this step used to look for.
	ctx.Step(`^some inspector names the runbook, the signature that does not verify, and the pin she is kept at$`, func(c context.Context) error {
		w := worldFrom(c)
		kept := j001900Of(w).pinBeforeSync
		if kept == "" {
			return fmt.Errorf("no pre-sync pin was captured — the sync step did not run, so there is no kept pin to look for")
		}
		if len(kept) > 16 {
			kept = kept[:16] // the pin is rendered abbreviated for humans
		}
		return j001900ProbesAnswered(w, j001900Bundle, "does not verify", kept)
	})

	ctx.Step(`^Alice asks ctxloom why the runbook stopped arriving$`, func(c context.Context) error {
		w := worldFrom(c)
		// Every inspector FLOWS-UNIFIED names for this hop, plus the two
		// plausible spellings a user would reach for. None of them is claimed
		// to exist; the assertion is that SOMETHING answers.
		j001900Probe(w, "doctor")
		j001900Probe(w, "review", "--list")
		j001900Probe(w, "bundle", "list")
		j001900Probe(w, "bundle", "show", j001900LockedName())
		j001900Probe(w, "trust", "signer", "list")
		return nil
	})

	// --- B3: attested -> distributed ----------------------------------------

	ctx.Step(`^Carol publishes a newer signed runbook while Alice's copy is held$`, func(c context.Context) error {
		w := worldFrom(c)
		// Held by its LOCKFILE name (remote-qualified): `bundle hold` resolves
		// against the active lockfile, and the bare name is not an entry there.
		if err := runOK(w, "bundle", "hold", j001900LockedName()); err != nil {
			return err
		}
		if err := j001900WriteRevised(w, j001900RevisedMarker); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "sign", j001900Bundle); err != nil {
			return err
		}
		return j001900PublishFromDisk(w)
	})

	ctx.Step(`^the installed-bundle listing names the runbook as held$`, func(c context.Context) error {
		return j001900OutputNamesAll(worldFrom(c), "the installed-bundle listing", j001900Bundle, "held")
	})

	ctx.Step(`^her assistant still receives the older deploy guidance and not the newer$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900AssertDelivery(w, j001900DeployMarker, true); err != nil {
			return err
		}
		return j001900AssertDelivery(w, j001900RevisedMarker, false)
	})

	// --- B4: distributed -> admitted ----------------------------------------

	ctx.Step(`^Carol published the signed runbook from a key Alice has never reviewed$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900Setup(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900DeployMarker); err != nil {
			return err
		}
		if err := runOK(w, "bundle", "sign", j001900Bundle); err != nil {
			return err
		}
		// Deliberately NOT trusted: this is B4's cause.
		if err := j001900PublishFromDisk(w); err != nil {
			return err
		}
		return j001900Reference(w)
	})

	ctx.Step(`^the pending list names the deploy fragment of the runbook$`, func(c context.Context) error {
		return j001900OutputNamesAll(worldFrom(c), "the pending-review list", j001900Bundle, j001900Fragment)
	})

	ctx.Step(`^the pending list says the runbook's signer is one she does not trust, naming the key$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001900OutputNamesAll(worldFrom(c), "the pending-review list", "untrusted", j001900Of(w).signer.Fingerprint())
	})

	ctx.Step(`^her assistant does not receive the deploy guidance$`, func(c context.Context) error {
		return j001900AssertDelivery(worldFrom(c), j001900DeployMarker, false)
	})

	// --- B5: admitted -> composed -------------------------------------------

	ctx.Step(`^the runbook is installed and admitted, but composed into no profile$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900Setup(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900DeployMarker); err != nil {
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
		if strings.Contains(out, j001900Bundle) {
			return fmt.Errorf("the profile listing names %q, but this scenario composed it into no profile; output:\n%s", j001900Bundle, out)
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
		return j001900OutputNamesAll(worldFrom(c), "the agent listing", "Profiles:", "- default")
	})

	ctx.Step(`^the dry run shows her the deploy guidance that would be composed$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if !strings.Contains(out, j001900DeployMarker) {
			return fmt.Errorf("`run --dry-run` did not show the composed context: the deploy guidance %q is nowhere in its output, "+
				"so it cannot answer 'is my content in the context this run would send?'. What it printed was:\n%s", j001900DeployMarker, out)
		}
		return nil
	})

	ctx.Step(`^the runbook is composed into Alice's profile$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900Setup(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900DeployMarker); err != nil {
			return err
		}
		return runOK(w, "profile", "modify", "default", "--add-bundle", j001900Bundle)
	})

	// --- B6: composed -> delivered ------------------------------------------

	ctx.Step(`^the engine's own surface on disk still holds last week's copy$`, func(c context.Context) error {
		w := worldFrom(c)
		// Materialize once (so the surface exists and is genuinely ctxloom's),
		// then let the CONTENT move on underneath it without re-materializing.
		// That is exactly "stale materialization": the composed context and the
		// file the engine will actually read have diverged.
		if _, err := j001900Delivered(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900RevisedMarker); err != nil {
			return err
		}
		body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return fmt.Errorf("read the materialized surface: %w", err)
		}
		if !strings.Contains(body, j001900DeployMarker) {
			return fmt.Errorf("the materialized surface does not hold last week's copy, so this scenario planted no staleness; it holds:\n%s", body)
		}
		if strings.Contains(body, j001900RevisedMarker) {
			return fmt.Errorf("the materialized surface already carries this week's copy — nothing re-materialized it, so the fixture is wrong; it holds:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^the wiring report names the materialized surface as stale$`, func(c context.Context) error {
		return j001900OutputNamesAll(worldFrom(c), "`manage check`", "stale")
	})

	// --- B7: delivered -> ingested ------------------------------------------

	ctx.Step(`^every earlier inspector is green and the deploy guidance is materialized into the engine's own surface$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900Setup(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900DeployMarker); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", j001900Bundle); err != nil {
			return err
		}
		return j001900AssertDelivery(w, j001900DeployMarker, true)
	})

	ctx.Step(`^Alice asks ctxloom what her engine actually ingested$`, func(c context.Context) error {
		w := worldFrom(c)
		// B7 has no inspector that can answer the ORIGINAL question anywhere in
		// the product (FLOWS-UNIFIED Appendix A.2, verdict DEFECT) — that never
		// changes, so there is still no single spelling to assert an answer
		// against. Probe every surface that could plausibly own it; the Then now
		// looks for the one that states the LIMIT instead of an answer.
		j001900Probe(w, "doctor")
		j001900Probe(w, "manage", "status")
		j001900Probe(w, "agent", "show", "default")
		j001900Probe(w, "session", "list")
		return nil
	})

	// cli.doctorCheckIngestionLimit (DOCTOR-CHECK-INGESTION-q7, an "info" line
	// beside SETUP-AUTHPING-j0) is the inspector that answers now — not by
	// naming what the engine read, which no inspector can ever do, but by
	// naming the limit itself. The three phrases below must all land in the
	// SAME probe's output: ctxloom's own "writes" (the act it can vouch for),
	// the "does not own" disclaimer (the boundary it cannot cross), and the
	// explicit "cannot confirm" (so the sentence cannot be read as a claim).
	// A product that started FABRICATING the claim this row used to demand —
	// asserting or implying the engine consumed what it was handed — would
	// drop all three and turn this red.
	ctx.Step(`^some inspector states plainly that it cannot confirm the engine read what ctxloom delivered$`, func(c context.Context) error {
		return j001900ProbesAnswered(worldFrom(c),
			"ctxloom writes the assembled context",
			"process ctxloom does not own",
			"nothing in this product can confirm it")
	})

	// --- M5: the two-machine symptom ----------------------------------------

	ctx.Step(`^Bob's checkout of the same project delivers the deploy guidance and Alice's does not$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900Setup(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900DeployMarker); err != nil {
			return err
		}
		// Bob's machine: the runbook composed into his profile, materialized,
		// and kept OUTSIDE Alice's project so the two are genuinely separate
		// deliveries rather than one file read twice.
		if err := runOK(w, "profile", "modify", "default", "--add-bundle", j001900Bundle); err != nil {
			return err
		}
		bobs, err := j001900Delivered(w)
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
		if err := runOK(w, "profile", "modify", "default", "--remove-bundle", j001900Bundle); err != nil {
			return err
		}
		return j001900AssertDelivery(w, j001900DeployMarker, false)
	})

	ctx.Step(`^Alice asks ctxloom to compare her delivered context with Bob's$`, func(c context.Context) error {
		w := worldFrom(c)
		bobFile := filepath.Join(w.env.Root, "bob-delivered", "CLAUDE.md")
		// M5 (FLOWS-UNIFIED §4 finding class (b)): every diagnostic ctxloom has
		// is single-machine. Probe the surfaces that would own a comparison.
		j001900Probe(w, "profile", "show", "default", "--compare", bobFile)
		j001900Probe(w, "doctor", "--compare", bobFile)
		j001900Probe(w, "profile", "materialize", "default", "--diff", bobFile)
		return nil
	})

	ctx.Step(`^ctxloom reports the deploy guidance as present for Bob and absent for her$`, func(c context.Context) error {
		return j001900ProbesAnswered(worldFrom(c), j001900DeployMarker)
	})
}
