//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// Distinctive marker strings j000300_source_augmentation.feature's sources ship as
// their "agent-setup" command content, so the composed interview prompt the
// mock engine (or a real @live assistant) receives can be checked for exactly
// this source's contribution — never a bare exit-code or file-exists proxy.
const (
	j000300CompanyOnboarding  = "J000300-COMPANY-ONBOARDING-STEPS-MARKER"
	j000300PersonalPreference = "J000300-PERSONAL-SETUP-PREFERENCE-MARKER"
	j000300BuiltinMarker      = "SCAN" // present verbatim in resources/prompts/agent-setup.md's built-in text
	j000300CompanionMarker    = "J000300-REPRISE-SETUP-GUIDANCE-MARKER"
	j000300CompanyCodeword    = "J000300-LIVE-COMPANY-CODEWORD"
	j000300CompanionCodeword  = "J000300-LIVE-COMPANION-CODEWORD"
)

func registerJ000300Steps(ctx *godog.ScenarioContext) {
	// --- repo bundles augment (mock) ----------------------------------------

	ctx.Step(`^her company's repository ships an "agent-setup" command with the company's onboarding steps$`, func(c context.Context) error {
		w := worldFrom(c)
		_, err := seedSource(w, "company", "commands", "agent-setup", j000300CompanyOnboarding, commandSourceYAML(j000300CompanyOnboarding), true, false)
		return err
	})

	ctx.Step(`^her personal repository ships an "agent-setup" command with her own setup preferences$`, func(c context.Context) error {
		w := worldFrom(c)
		_, err := seedSource(w, "personal", "commands", "agent-setup", j000300PersonalPreference, commandSourceYAML(j000300PersonalPreference), true, false)
		return err
	})

	ctx.Step(`^both repositories are trusted, each signed with its owner's key$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "mock", "mock"); err != nil {
			return err
		}
		recordFile := filepath.Join(w.env.Root, "mock-record.txt")
		w.env.SetEnv("CTXLOOM_MOCK_RECORD_FILE", recordFile)
		w.j000300RecordFile = recordFile
		for _, name := range []string{"company", "personal"} {
			src := w.j000200Sources[name]
			if src == nil {
				return fmt.Errorf("source %q was never seeded", name)
			}
			if err := w.env.TrustSigner(src.signer, name+"@example.com", true); err != nil {
				return err
			}
			if err := addSourceAsRemote(w, name, "default"); err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^Alice runs the ctxloom setup$`, func(c context.Context) error {
		// No-op beyond what "both repositories are trusted" (or "the reprise
		// companion is installed") already scaffolded: a project whose default
		// LLM is the mock backend, with sources already trusted/pulled (or the
		// fake companion already on PATH). The NEXT step drives the actual
		// `ctxloom init` discovery launch. Split into two steps to mirror the
		// Gherkin's own two-beat "runs setup" / "launches a mock engine" framing.
		return nil
	})

	ctx.Step(`^it launches a mock engine for the configuration interview$`, func(c context.Context) error {
		w := worldFrom(c)
		recorded, err := driveDiscoverySessionViaMock(w, w.j000300RecordFile)
		if err != nil {
			return err
		}
		w.j000300Recorded = recorded
		return nil
	})

	ctx.Step(`^the interview prompt the mock engine receives includes ctxloom's built-in setup guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		prompt, err := promptSection(w.j000300Recorded)
		if err != nil {
			return err
		}
		// Real evidence: the mock's recorded prompt was already attached to
		// "it launches a mock engine for the configuration interview" (the
		// step that actually launched it), so this Then — which re-inspects
		// the same recorded prompt without running anything new — needs its
		// own excerpt re-attached, or its evidence pane renders empty.
		w.docStepMaterialized = j000400Excerpt(prompt, j000300BuiltinMarker, 2)
		if !strings.Contains(prompt, j000300BuiltinMarker) {
			return fmt.Errorf("interview prompt does not contain the built-in guidance marker %q; prompt:\n%s", j000300BuiltinMarker, prompt)
		}
		return nil
	})

	ctx.Step(`^it includes the company's onboarding steps$`, func(c context.Context) error {
		w := worldFrom(c)
		prompt, err := promptSection(w.j000300Recorded)
		if err != nil {
			return err
		}
		w.docStepMaterialized = j000400Excerpt(prompt, j000300CompanyOnboarding, 2)
		if !strings.Contains(prompt, j000300CompanyOnboarding) {
			return fmt.Errorf("interview prompt does not contain the company's onboarding steps; prompt:\n%s", prompt)
		}
		return nil
	})

	ctx.Step(`^it includes her personal setup preferences$`, func(c context.Context) error {
		w := worldFrom(c)
		prompt, err := promptSection(w.j000300Recorded)
		if err != nil {
			return err
		}
		w.docStepMaterialized = j000400Excerpt(prompt, j000300PersonalPreference, 2)
		if !strings.Contains(prompt, j000300PersonalPreference) {
			return fmt.Errorf("interview prompt does not contain her personal setup preferences; prompt:\n%s", prompt)
		}
		return nil
	})

	// --- installed companion augments (mock) --------------------------------

	ctx.Step(`^the "reprise" companion is installed$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "mock", "mock"); err != nil {
			return err
		}
		recordFile := filepath.Join(w.env.Root, "mock-record.txt")
		w.env.SetEnv("CTXLOOM_MOCK_RECORD_FILE", recordFile)
		w.j000300RecordFile = recordFile
		return nil
	})

	ctx.Step(`^it outputs its own setup guidance through ctxloom's setup-prompt CLI contract$`, func(c context.Context) error {
		w := worldFrom(c)
		bundleYAML := commandSourceYAML(j000300CompanionMarker)
		envelope, err := signing.EncodeLoadoutEnvelope([]byte(bundleYAML), nil, "")
		if err != nil {
			return fmt.Errorf("encode fake companion loadout envelope: %w", err)
		}
		versionJSON := `{"name":"reprise","version":"9.9.9-j000200-fake"}`
		return w.env.InstallFakeCompanion("reprise", versionJSON, string(envelope))
	})

	// "Alice runs the ctxloom setup" / "it launches a mock engine..." /
	// "the interview prompt ... includes ctxloom's built-in setup guidance"
	// are already registered above and apply verbatim to this scenario too.

	ctx.Step(`^it includes reprise's setup guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		prompt, err := promptSection(w.j000300Recorded)
		if err != nil {
			return err
		}
		w.docStepMaterialized = j000400Excerpt(prompt, j000300CompanionMarker, 2)
		if !strings.Contains(prompt, j000300CompanionMarker) {
			return fmt.Errorf("interview prompt does not contain reprise's setup guidance; prompt:\n%s", prompt)
		}
		return nil
	})

	// --- @live twins ----------------------------------------------------------

	ctx.Step(`^her company's "agent-setup" command instructs the assistant to confirm a company codeword$`, func(c context.Context) error {
		w := worldFrom(c)
		a, ok := liveAgents["claude"]
		if !ok || !liveAgentAvailable(a) {
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
		command := commandSourceYAML(fmt.Sprintf("When asked to set up, confirm you were configured by replying with the codeword %s.", j000300CompanyCodeword))
		_, err := seedSource(w, "company", "commands", "agent-setup", j000300CompanyCodeword, command, true, true)
		return err
	})

	// Same shape as steps_j000200_setup.go's live scenario — these
	// re-guard on w.j000200Live, redundant with the scenario's own first Given
	// already skipping via ErrSkip when the live agent isn't available.
	// Failing loud instead of re-skipping turns "should be unreachable" into
	// a real invariant should a future reorder or step-text reuse ever reach
	// one of these with w.j000200Live still false.
	ctx.Step(`^the company repository is trusted, signed with the company key$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000300: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		return addSourceAsRemote(w, "company", "default")
	})

	ctx.Step(`^Alice runs the ctxloom setup and its interview launches her real assistant$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000300: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		// The composed agent-setup guidance (built-in + the company command's
		// codeword instruction) is exactly what `ctxloom init prompt` emits
		// (internal/cli/agent.go, via the SAME operations.ResolveSetupPrompt
		// this scenario is proving) — driving it straight into the real
		// assistant as its prompt is the equivalent of the interactive
		// discovery session launching it, without needing a real pty here.
		if err := runOK(w, "agent", "setup"); err != nil {
			return err
		}
		guidance := w.env.LastOutput()
		_ = w.env.Run("run", "--print", "--profile", "default", guidance)
		return nil
	})

	ctx.Step(`^the assistant's setup response confirms the company codeword$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000300: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		if !strings.Contains(w.env.LastOutput(), j000300CompanyCodeword) {
			return fmt.Errorf("assistant's setup response does not confirm the company codeword; output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^its setup guidance instructs the assistant to confirm a companion codeword$`, func(c context.Context) error {
		w := worldFrom(c)
		a, ok := liveAgents["claude"]
		if !ok || !liveAgentAvailable(a) {
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
		bundleYAML := commandSourceYAML(fmt.Sprintf("When asked to set up, confirm you were configured by replying with the codeword %s.", j000300CompanionCodeword))
		envelope, err := signing.EncodeLoadoutEnvelope([]byte(bundleYAML), nil, "")
		if err != nil {
			return err
		}
		return w.env.InstallFakeCompanion("reprise", `{"name":"reprise","version":"9.9.9-j000200-fake"}`, string(envelope))
	})

	ctx.Step(`^the assistant's setup response confirms the companion codeword$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000300: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		if !strings.Contains(w.env.LastOutput(), j000300CompanionCodeword) {
			return fmt.Errorf("assistant's setup response does not confirm the companion codeword; output:\n%s", w.env.LastOutput())
		}
		return nil
	})
}
