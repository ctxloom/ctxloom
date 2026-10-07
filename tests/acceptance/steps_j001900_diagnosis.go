//go:build acceptance

// J001900: "the day the assistant goes blind" (j001900_diagnosis.feature) — the
// DIAGNOSIS walk. FLOWS-UNIFIED.md's U5, numbered J001900 here because that
// document's "proposed J001400" slot was taken by bundle distribution before this
// was written.
//
// THE SPINE IS THE BOUNDARY TABLE (FLOWS-UNIFIED.md Appendix A.2). Content
// travels authored → packaged → distributed → admitted → composed →
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
// capability — inverted to assert instead that ctxloom SAYS it
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
// ISOLATION: everything runs through testenv.TestEnvironment's isolated
// HOME/XDG.
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
)

// j001900State is this journey's fixture state.
type j001900State struct {
	ready bool

	// Remote bookkeeping for the boundaries that need a real publish hop.
	bare       string
	url        string
	referenced bool

	// probes records every invocation an "Alice asks ctxloom ..." step tried
	// while looking for an inspector that does not exist, so the failure
	// message can name exactly what was probed rather than asserting against
	// one guessed spelling. See j001900Probe.
	probes []j001900ProbeResult
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

// j001900EnvelopeYAML renders the runbook's envelope at a given version; the
// deploy process lives in a file beside it (j001900FragmentBody).
//
// The VERSION is a parameter because it is what makes a republish an actual
// PIN ADVANCE. Measured while writing this journey: editing a bundle's content
// while leaving `version:` alone republishes bytes that `deps check` never
// advances to, so the consumer keeps receiving the previous copy. A republish
// that is meant to be seen has to produce a pin advance.
func j001900EnvelopeYAML(version string) string {
	return fmt.Sprintf("version: %q\n", version)
}

// j001900FragmentBody renders the deploy-process fragment FILE, read by the
// same tree reader as steps_j001400_bundle_distribution.go's j001400AuthoredTree.
//
// NO front-matter description: a fragment's `description` IS its PREMISE
// (content.ItemMeta.Description), and a premised fragment is withheld from an
// ordinary assembly rather than delivered. This journey asserts the deploy
// guidance ARRIVES, so it must be unconditional.
func j001900FragmentBody(content string) string {
	return content + "\n"
}

func j001900BundlePath() string { return bundleFilePath(j001900Bundle) }

// j001900FragmentPath is the deploy-process fragment's path inside the
// runbook's own tree.
func j001900FragmentPath() string {
	return treeBundleItemPath(j001900Bundle, "fragments/"+j001900Fragment+".md")
}

// j001900Setup is the Background: a hermetic project with one engine, a seed
// bundle and a "default" profile.
func j001900Setup(w *World) error {
	st := j001900Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(w.env.ProjectDir, filepath.FromSlash(testenv.BundlesRoot())), 0o755); err != nil {
		return fmt.Errorf("create authored bundles dir: %w", err)
	}

	st.ready = true
	return nil
}

// j001900WriteAuthored writes the runbook into the authored tree at version
// 1.0.0 — Friday's copy, and the one every locally-authored scenario uses. The
// envelope carries no items; the deploy process lives in the fragment file
// beside it (see j001900EnvelopeYAML).
func j001900WriteAuthored(w *World, content string) error {
	if err := w.env.WriteFile(j001900BundlePath(), j001900EnvelopeYAML("1.0.0")); err != nil {
		return err
	}
	return w.env.WriteFile(j001900FragmentPath(), j001900FragmentBody(content))
}

// j001900WriteRevised writes Carol's revision at a HIGHER version, so republishing
// it is a genuine pin advance rather than a byte change the consumer's lock
// never moves to. See j001900EnvelopeYAML. It touches the SAME two files
// j001900WriteAuthored wrote.
func j001900WriteRevised(w *World, content string) error {
	if err := w.env.WriteFile(j001900BundlePath(), j001900EnvelopeYAML("1.1.0")); err != nil {
		return err
	}
	return w.env.WriteFile(j001900FragmentPath(), j001900FragmentBody(content))
}

// j001900LockedName is the name the runbook carries in the active lockfile once it
// has been pulled from the team remote — remote-qualified, which is what
// `deps hold` resolves against.
func j001900LockedName() string { return "team/" + j001900Bundle }

// j001900PublishFromDisk publishes the WHOLE authored tree — the envelope and
// the fragment file — into the team remote, then hands the authoring copy off
// out of the project.
//
// The hand-off is load-bearing: one hermetic project plays both the publishing
// and the consuming checkout, and a LOCAL authored bundle of the same name
// shadows the remote one. Left in place, every delivery assertion in this
// journey would silently measure the local copy and pass while proving
// nothing.
func j001900PublishFromDisk(w *World) error {
	st := j001900Of(w)
	localDir := filepath.Join(w.env.ProjectDir, filepath.FromSlash(treeBundlePath(j001900Bundle)))
	remoteRoot := treeBundlePath(j001900Bundle)

	files := map[string]string{}
	walkErr := filepath.WalkDir(localDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(localDir, p)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files[remoteRoot+"/"+filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("collect the authored %q tree to publish: %w", j001900Bundle, walkErr)
	}
	if len(files) == 0 {
		return fmt.Errorf("the authored %q tree at %s holds no files to publish", j001900Bundle, localDir)
	}

	if err := os.RemoveAll(localDir); err != nil {
		return fmt.Errorf("hand off the authored %q tree: %w", j001900Bundle, err)
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
// way — remote create (registering it is the trust act), reference it from the composed
// profile, pull — and is idempotent so a scenario can pull again after the
// publisher advances.
func j001900Reference(w *World) error {
	st := j001900Of(w)
	if st.referenced {
		return runOK(w, "deps", "pull")
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
	return runOK(w, "deps", "pull")
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

// j001900EveryProbeAnswered is j001900ProbesAnswered's STRICT twin: every
// probed invocation must report all of wants, not merely one of them.
//
// A step whose prose is "nothing tells her" must not be satisfiable by exactly
// one of three surfaces telling her. That is not hypothetical: wiring `agent
// show` alone turned J002000's row green while `doctor` and `manage check` —
// two of the three surfaces the same step probes, and the two a user is far
// likelier to run — still said nothing, so the row passed over a gap that was
// half present (trusting-ambiguity).
//
// The any-of form stays right for a question whose OWNER is genuinely
// undecided: there, demanding every inspector answer would be demanding
// duplication. Use this one when each probed surface is one the scenario
// names as owing the answer.
func j001900EveryProbeAnswered(w *World, wants ...string) error {
	st := j001900Of(w)
	if len(st.probes) == 0 {
		return fmt.Errorf("no inspector was probed — the step that asks the question did not run")
	}
	var silent []j001900ProbeResult
	for _, p := range st.probes {
		for _, want := range wants {
			if !strings.Contains(p.output, want) {
				silent = append(silent, p)
				break
			}
		}
	}
	if len(silent) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d of the %d probed invocations did not report all of %v. Each one is a surface a user would "+
		"reasonably run to find this out, so a row that only ONE of them answers leaves the others silently broken:\n",
		len(silent), len(st.probes), wants)
	for _, p := range silent {
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

// j001900OutputNamesAll asserts the LAST command's stdout names every one of
// wants — the shape of "the listing/report named the item". Stdout alone,
// because every caller asserts a rendered LISTING: a stderr advisory that
// quotes the same bundle or fragment name would otherwise satisfy a listing
// that rendered nothing. Probes that accept ANY inspector's answer, stderr
// included, go through j001900ProbesAnswered instead.
func j001900OutputNamesAll(w *World, what string, wants ...string) error {
	return j001900NamesAll(w.env.LastStdout(), what, wants...)
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

func registerJ001900Steps(ctx *godog.ScenarioContext) {
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

	// --- Publishing the runbook ---------------------------------------------

	ctx.Step(`^Carol published the runbook, and Alice's assistant receives its deploy guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900Setup(w); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900DeployMarker); err != nil {
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

	ctx.Step(`^Alice syncs on Monday$`, func(c context.Context) error {
		w := worldFrom(c)
		// An ordinary sync: no incident ceremony, no special flag. Its exit
		// codes are not the assertion — what arrives afterwards is. `deps
		// upgrade --yes` is the one command that advances a pin, so it is what
		// a person runs when they want Monday's content.
		_ = w.env.Run("deps", "check")
		_ = w.env.Run("deps", "pull")
		_ = w.env.Run("--format", "text", "deps", "upgrade", "--yes")
		return nil
	})

	// --- B3: packaged -> distributed ----------------------------------------

	ctx.Step(`^Carol publishes a newer runbook while Alice's copy is held$`, func(c context.Context) error {
		w := worldFrom(c)
		// Held by its LOCKFILE name (remote-qualified): `deps hold` resolves
		// against the active lockfile, and the bare name is not an entry there.
		if err := runOK(w, "deps", "hold", j001900LockedName()); err != nil {
			return err
		}
		if err := j001900WriteRevised(w, j001900RevisedMarker); err != nil {
			return err
		}
		return j001900PublishFromDisk(w)
	})

	ctx.Step(`^the installed-bundle listing names the runbook as held$`, func(c context.Context) error {
		w := worldFrom(c)
		if !formatAskedFor(w).Structured() {
			return j001900OutputNamesAll(w, "the installed-bundle listing", j001900Bundle, "held")
		}
		// The predicate selector needs an EXACT Name match, and a remote-sourced
		// bundle's Name is its full canonical ask ("file:///.../remote.git@bundles/deploy-runbook"),
		// not the bare bundle name — so this scans for the entry whose Name
		// carries the runbook rather than addressing one exactly by it.
		entries, err := lastOutputJSONArray(w, "$")
		if err != nil {
			return fmt.Errorf("%v; stdout:\n%s", err, w.env.LastStdout())
		}
		for _, e := range entries {
			name, err := jsonAtPath(e, "name")
			if err != nil {
				continue
			}
			got, ok := jsonScalar(name)
			if !ok || !strings.Contains(got, j001900Bundle) {
				continue
			}
			held, err := jsonAtPath(e, "held")
			if err != nil {
				return fmt.Errorf("%v; stdout:\n%s", err, w.env.LastStdout())
			}
			if hs, _ := jsonScalar(held); hs != "true" {
				return fmt.Errorf("the installed-bundle JSON listing's %q entry has held=%s, want true; stdout:\n%s",
					got, hs, w.env.LastStdout())
			}
			return nil
		}
		return fmt.Errorf("no entry in the installed-bundle JSON listing names %q; stdout:\n%s", j001900Bundle, w.env.LastStdout())
	})

	ctx.Step(`^her assistant still receives the older deploy guidance and not the newer$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001900AssertDelivery(w, j001900DeployMarker, true); err != nil {
			return err
		}
		return j001900AssertDelivery(w, j001900RevisedMarker, false)
	})

	// --- B4: distributed -> admitted ----------------------------------------

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

	// STRENGTHENED. This asserted only that "default" appeared
	// SOMEWHERE in `agent show default`'s output — and the agent in this
	// fixture is ITSELF named "default", so the assertion was satisfied by the
	// echoed argument and could not tell the agent's own name from the profile
	// it composes. Measured: deleting writeBulletList(w, "Profiles", …) from
	// renderAgentShow entirely left this step GREEN. It now reads the rendered
	// section — writeBulletList's "Profiles:" heading plus the "- default"
	// bullet under it — so that same deletion turns it red.
	ctx.Step(`^the agent listing names the profile it composes$`, func(c context.Context) error {
		w := worldFrom(c)
		if !formatAskedFor(w).Structured() {
			return j001900OutputNamesAll(w, "the agent listing", "Profiles:", "- default")
		}
		entries, err := lastOutputJSONArray(w, "definition.profiles")
		if err != nil {
			return fmt.Errorf("%v; stdout:\n%s", err, w.env.LastStdout())
		}
		for _, e := range entries {
			if got, ok := jsonScalar(e); ok && got == "default" {
				return nil
			}
		}
		return fmt.Errorf("the agent JSON listing's definition.profiles does not name %q; stdout:\n%s", "default", w.env.LastStdout())
	})

	ctx.Step(`^the dry run shows her the deploy guidance that would be composed$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastStdout()
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
		// Materialize onto the PROJECT ROOT itself (target "."), not the
		// portable "out" tree j001900Delivered uses everywhere else in this
		// journey. That "out" convention exists for scenarios that only ever
		// READ the delivered payload back through the SAME command — it is a
		// stand-in for "wherever the assembled bytes end up", and nothing else
		// in the product ever looks there. `manage check`'s new staleness
		// report (operations.surfaceCurrencies) reads claude-code's NATIVE
		// context surface at the project root (CLAUDE.md via
		// contextSurface.State) — the real-world shape of a user who chose
		// claude's static-file context delivery over the SessionStart hook.
		// Materializing there is what makes "the engine's own surface on disk"
		// a surface `manage check` can actually observe, matching the
		// production code this scenario exists to exercise rather than an
		// export tree nothing inspects.
		if _, err := materializeDefault(w, "."); err != nil {
			return err
		}
		if err := j001900WriteAuthored(w, j001900RevisedMarker); err != nil {
			return err
		}
		body, err := w.env.ReadFile("CLAUDE.md")
		if err != nil {
			return fmt.Errorf("read the materialized surface: %w", err)
		}
		if !strings.Contains(body, j001900DeployMarker) {
			return fmt.Errorf("the materialized surface does not hold last week's copy, so this scenario planted no staleness; it holds:\n%s", body)
		}
		if strings.Contains(body, j001900RevisedMarker) {
			return fmt.Errorf("the materialized surface already carries this week's copy — nothing re-materialized it, so the fixture is wrong; it holds:\n%s", body)
		}
		w.docStepMaterialized = "CLAUDE.md (project root, before `manage check` runs):\n" + body
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
		j001900Probe(w, "manage", "check")
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
