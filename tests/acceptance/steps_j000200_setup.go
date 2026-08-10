//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// Distinctive marker strings j000200_setup.feature's fragment-carrying sources
// ship, so a materialized/assembled context or a mock engine's recorded input
// can be checked for exactly this source's payload — never a bare file-exists
// or exit-code proxy (ASSERTION DISCIPLINE).
const (
	j000200PersonalMarker   = "J000200-PERSONAL-REPO-CONTEXT-MARKER"
	j000200CompanyMarker    = "J000200-COMPANY-REPO-CONTEXT-MARKER"
	j000200ThirdPartyMarker = "J000200-THIRDPARTY-REPO-CONTEXT-MARKER"
	j000200HeldFirstMarker  = "J000200-HELD-FIRST-CONTEXT-MARKER"
	j000200HeldSecondMarker = "J000200-HELD-SECOND-CONTEXT-MARKER"
	j000200LivePersonalMark = "J000200-LIVE-PERSONAL-MARKER-PHRASE"
	j000200LiveCompanyMark  = "J000200-LIVE-COMPANY-MARKER-PHRASE"
)

func registerJ000200SetupSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice has a fresh project directory$`, func(c context.Context) error {
		return worldFrom(c).env.InitGitRepo()
	})

	ctx.Step(`^her personal ctxloom repository is signed with her own key$`, func(c context.Context) error {
		w := worldFrom(c)
		// "Signed with her OWN key" carries the same self-evident trust a real
		// publish-then-trust-your-own-key pair of commands would establish for
		// content you author yourself (docs/trust-model.md: "sign your bundles
		// and trust your own signing key") — this fixture step performs both
		// halves (seedSource signs; TrustSigner trusts), exactly as the Then
		// step's "because it is signed with her own key" requires for the
		// content to actually reach the assembled context.
		_, err := seedSource(w, "personal", "fragments", "marker", j000200PersonalMarker, fragmentSourceYAML(j000200PersonalMarker), true, true)
		return err
	})

	ctx.Step(`^her company's ctxloom repository is signed with the company key, which Alice trusts$`, func(c context.Context) error {
		w := worldFrom(c)
		_, err := seedSource(w, "company", "fragments", "marker", j000200CompanyMarker, fragmentSourceYAML(j000200CompanyMarker), true, true)
		return err
	})

	ctx.Step(`^Alice runs the ctxloom setup for (\S+)$`, func(c context.Context, engine string) error {
		return ensureProjectWithEngine(worldFrom(c), engine, engine)
	})

	ctx.Step(`^she adds her personal repository as a source$`, func(c context.Context) error {
		return addSourceAsRemote(worldFrom(c), "personal", "default")
	})

	ctx.Step(`^she adds her company's repository as a source$`, func(c context.Context) error {
		return addSourceAsRemote(worldFrom(c), "company", "default")
	})

	ctx.Step(`^she adds her personal and company repositories as sources$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := addSourceAsRemote(w, "personal", "default"); err != nil {
			return err
		}
		return addSourceAsRemote(w, "company", "default")
	})

	ctx.Step(`^her project is configured for (\S+)$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		if err := runOK(w, "config", "show"); err != nil {
			return err
		}
		if !strings.Contains(w.env.LastOutput(), engine) {
			return fmt.Errorf("config show does not mention engine %q; output:\n%s", engine, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^her personal repository's context is part of her configuration, because it is signed with her own key$`, func(c context.Context) error {
		return assertMaterializedContains(c, "out", j000200PersonalMarker)
	})

	ctx.Step(`^her company repository's context is part of her configuration, because she trusts the company key$`, func(c context.Context) error {
		return assertMaterializedContains(c, "out", j000200CompanyMarker)
	})

	// --- Scenario 2: setup configures, a restart delivers -------------------
	//
	// LOCKED text says init's discovery session composes fragments and a
	// restart delivers them into a real session. init itself has no relaunch
	// prompt to drive anymore (offerSessionRelaunch was deleted, init-as-skill
	// slice ④ — init hands off to the setup session's raw CLI/TUI and exits;
	// re-entry is `/ctxloom-init` or an ACP client, not a relaunch loop), so
	// what these two steps need to prove is narrower than it once was: a
	// freshly launched `ctxloom run` against the project the sources were
	// added to IS a restart in every observable sense (a brand-new engine
	// subprocess, resolving the SAME composed profile fresh) — via
	// runFreshMockSession. See that function's doc for the exact substitution.
	ctx.Step(`^the setup interview composes her agents' profiles from the sources' fragments$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runOK(w, "profile", "show", "default"); err != nil {
			return err
		}
		out := w.env.LastOutput()
		// A pulled remote bundle canonicalizes to its full seeded URL
		// (file:///.../remote.git@bundles/src), not the short "<remote>/<bundle>"
		// form used to ADD it — check for each source's canonical ref rather
		// than the short form.
		for _, name := range []string{"personal", "company"} {
			ref := w.remoteBare[name] + "@bundles/src"
			if !strings.Contains(out, ref) {
				return fmt.Errorf("profile default does not yet compose %q; profile show output:\n%s", ref, out)
			}
		}
		return nil
	})

	ctx.Step(`^ctxloom offers to restart into her newly configured session$`, func(c context.Context) error {
		// See the scenario-level comment above: the harness substitutes a
		// freshly launched `ctxloom run` for what this Gherkin line describes.
		// Nothing to set up here beyond what "adds ... as sources" already
		// did; this step and the next act as the seam where a future harness
		// revision could swap in the real interactive flow.
		return nil
	})

	ctx.Step(`^Alice accepts the restart$`, func(c context.Context) error {
		w := worldFrom(c)
		recorded, err := runFreshMockSession(w)
		if err != nil {
			return err
		}
		w.j000200RestartRecorded = recorded
		return nil
	})

	ctx.Step(`^the restarted mock engine receives her personal repository's fragments$`, func(c context.Context) error {
		w := worldFrom(c)
		// Real evidence: the mock's recorded prompt was already attached to
		// "Alice accepts the restart" (whichever step set w.j000200RestartRecorded);
		// re-excerpt it here around this step's own marker so THIS assertion's
		// evidence pane shows what it actually checked, not a blank inherited
		// from the set-and-consume field already being spent.
		w.docStepMaterialized = j000400Excerpt(w.j000200RestartRecorded, j000200PersonalMarker, 2)
		if !strings.Contains(w.j000200RestartRecorded, j000200PersonalMarker) {
			return fmt.Errorf("mock's recorded input does not contain the personal repository's marker; recorded:\n%s", w.j000200RestartRecorded)
		}
		return nil
	})

	ctx.Step(`^the restarted mock engine receives her company repository's fragments$`, func(c context.Context) error {
		w := worldFrom(c)
		w.docStepMaterialized = j000400Excerpt(w.j000200RestartRecorded, j000200CompanyMarker, 2)
		if !strings.Contains(w.j000200RestartRecorded, j000200CompanyMarker) {
			return fmt.Errorf("mock's recorded input does not contain the company repository's marker; recorded:\n%s", w.j000200RestartRecorded)
		}
		return nil
	})

	// --- Scenario 3: @live, restarted assistant sees every source -----------

	ctx.Step(`^her personal and company repositories are trusted, signed sources$`, func(c context.Context) error {
		w := worldFrom(c)
		a, ok := liveAgents["claude"]
		if !ok {
			return fmt.Errorf("no live agent config registered for claude")
		}
		if !liveAgentAvailable(a) {
			return godog.ErrSkip
		}
		w.j000200Live = true
		cfg := a.config + "agents:\n  default:\n    engine: claude\n    profiles:\n      - default\n" + "default_agent: default\n"
		if err := scaffoldProjectWithConfig(w, cfg); err != nil {
			return err
		}
		if err := seedLiveCredentials("claude", a, realHomeDir, w.env.HomeDir, w.env.SetChildEnv); err != nil {
			return err
		}
		if _, err := seedSource(w, "personal", "fragments", "marker", j000200LivePersonalMark, fragmentSourceYAML(j000200LivePersonalMark), true, true); err != nil {
			return err
		}
		_, err := seedSource(w, "company", "fragments", "marker", j000200LiveCompanyMark, fragmentSourceYAML(j000200LiveCompanyMark), true, true)
		return err
	})

	ctx.Step(`^each source carries a distinct marker phrase$`, func(c context.Context) error {
		// The markers were baked into each source's fragment content by the
		// preceding step (seedSource); nothing further to arrange. Kept as its
		// own step (rather than folded into the previous one) to mirror the
		// Gherkin's own two-beat framing.
		return nil
	})

	// The four Then/And steps below used to re-guard on w.j000200Live
	// with `return godog.ErrSkip`, redundant with (and hiding behind) the
	// scenario's own first Given already skipping via ErrSkip when the live
	// agent isn't available (godog stops running a scenario's remaining
	// steps once one returns ErrSkip). Under correct execution these guards
	// can never fire at all; if a future reorder or step-text reuse ever DID
	// reach one with w.j000200Live still false, silently re-skipping would hide
	// that break. Failing loud instead turns "this should be unreachable"
	// into a real invariant.
	ctx.Step(`^Alice has completed setup and restarted into her configured session$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000200: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		if err := addSourceAsRemote(w, "personal", "default"); err != nil {
			return err
		}
		return addSourceAsRemote(w, "company", "default")
	})

	ctx.Step(`^she asks her assistant to repeat every marker phrase it can see$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000200: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		_ = w.env.Run("run", "--one-shot", "--profile", "default",
			"Please repeat, verbatim and in full, every distinct marker phrase you can see in your context. Output nothing else.")
		return nil
	})

	ctx.Step(`^its reply contains her personal repository's marker$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000200: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		if !strings.Contains(w.env.LastOutput(), j000200LivePersonalMark) {
			return fmt.Errorf("assistant reply does not contain the personal marker; output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^its reply contains her company repository's marker$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000200: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		if !strings.Contains(w.env.LastOutput(), j000200LiveCompanyMark) {
			return fmt.Errorf("assistant reply does not contain the company marker; output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	// --- Scenario 4: held content, unsigned or untrusted-key -----------------

	ctx.Step(`^a third-party ctxloom repository whose content is (.+)$`, func(c context.Context, trustState string) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		switch trustState {
		case "unsigned":
			_, err := seedSource(w, "thirdparty", "fragments", "marker", j000200ThirdPartyMarker, fragmentSourceYAML(j000200ThirdPartyMarker), false, false)
			return err
		case "signed with a key Alice does not trust":
			_, err := seedSource(w, "thirdparty", "fragments", "marker", j000200ThirdPartyMarker, fragmentSourceYAML(j000200ThirdPartyMarker), true, false)
			return err
		default:
			return fmt.Errorf("unknown trust_state %q", trustState)
		}
	})

	ctx.Step(`^Alice adds it as a source$`, func(c context.Context) error {
		return addSourceAsRemote(worldFrom(c), "thirdparty", "default")
	})

	ctx.Step(`^Alice starts a session$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("profile", "materialize", "default", "--target", "out")
		return nil
	})

	ctx.Step(`^her assistant does not receive that repository's content$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile("out/CLAUDE.md")
		if err != nil {
			return fmt.Errorf("read materialized out/CLAUDE.md: %w", err)
		}
		w.docStepMaterialized = body // the delivered context, proving the held marker is absent
		if strings.Contains(body, j000200ThirdPartyMarker) {
			return fmt.Errorf("materialized context unexpectedly contains the held third-party marker:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^Alice is told the content is held for her review$`, func(c context.Context) error {
		w := worldFrom(c)
		// "Alice starts a session" (the preceding When) already ran the
		// materialize command that produced this output, so the automatic
		// CLIOutput attribution landed there, not here — re-attach the actual
		// terminal text this Then is checking.
		w.docStepMaterialized = strings.TrimSpace(w.env.LastOutput())
		if !strings.Contains(w.env.LastOutput(), "awaiting review") {
			return fmt.Errorf("materialize output does not tell Alice anything is awaiting review; output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	// --- Scenario 5: review held content item by item ------------------------

	ctx.Step(`^two sources are held for Alice's review$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		if _, err := seedSource(w, "first", "fragments", "marker", j000200HeldFirstMarker, fragmentSourceYAML(j000200HeldFirstMarker), false, false); err != nil {
			return err
		}
		if err := addSourceAsRemote(w, "first", "default"); err != nil {
			return err
		}
		if _, err := seedSource(w, "second", "fragments", "marker", j000200HeldSecondMarker, fragmentSourceYAML(j000200HeldSecondMarker), false, false); err != nil {
			return err
		}
		return addSourceAsRemote(w, "second", "default")
	})

	ctx.Step(`^Alice reviews the held content$`, func(c context.Context) error {
		return runOK(worldFrom(c), "review", "--list")
	})

	ctx.Step(`^she is shown each held item and where it came from$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		// "Alice reviews the held content" (the preceding When) is the step
		// that actually ran `review --list`, so it — not this Then — got the
		// automatic CLIOutput attribution; re-attach the real listing here.
		w.docStepMaterialized = strings.TrimSpace(out)
		for _, want := range []string{"first", "second", "fragments/marker"} {
			if !strings.Contains(out, want) {
				return fmt.Errorf("review --list does not mention %q; output:\n%s", want, out)
			}
		}
		return nil
	})

	ctx.Step(`^she approves the first and rejects the second$`, func(c context.Context) error {
		w := worldFrom(c)
		first := w.j000200Sources["first"]
		second := w.j000200Sources["second"]
		if err := runOK(w, "bundle", "trust", "file://"+w.remoteBare["first"]+"@bundles/"+first.bundleName+"#fragments/"+first.itemName); err != nil {
			return err
		}
		return runOK(w, "bundle", "reject", "file://"+w.remoteBare["second"]+"@bundles/"+second.bundleName+"#fragments/"+second.itemName)
	})

	ctx.Step(`^Alice starts a new session$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("profile", "materialize", "default", "--target", "out2")
		return nil
	})

	ctx.Step(`^her assistant receives the item she approved$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile("out2/CLAUDE.md")
		if err != nil {
			return fmt.Errorf("read materialized out2/CLAUDE.md: %w", err)
		}
		w.docStepMaterialized = body // the delivered context after review: approved marker present, rejected absent
		if !strings.Contains(body, j000200HeldFirstMarker) {
			return fmt.Errorf("materialized context does not contain the approved item's marker:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^her assistant never receives the item she rejected$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile("out2/CLAUDE.md")
		if err != nil {
			return fmt.Errorf("read materialized out2/CLAUDE.md: %w", err)
		}
		// This is a negative assertion — there is no rejected-marker text to
		// excerpt from the delivered context, because it is not there. The
		// real, observed evidence for an absence is what search for it found:
		// zero matches, next to the approved item's own excerpt for context.
		w.docStepMaterialized = fmt.Sprintf(
			"out2/CLAUDE.md: 0 matches for the rejected item's marker %q; delivered content around the approved item instead:\n%s",
			j000200HeldSecondMarker, j000400Excerpt(body, j000200HeldFirstMarker, 2))
		if strings.Contains(body, j000200HeldSecondMarker) {
			return fmt.Errorf("materialized context unexpectedly contains the rejected item's marker:\n%s", body)
		}
		return nil
	})
}

// assertMaterializedContains materializes the "default" profile into target
// and asserts the result contains want.
func assertMaterializedContains(c context.Context, target, want string) error {
	w := worldFrom(c)
	body, err := materializeDefault(w, target)
	if err != nil {
		return err
	}
	if !strings.Contains(body, want) {
		return fmt.Errorf("materialized %s/CLAUDE.md does not contain %q; content:\n%s", target, want, body)
	}
	return nil
}
