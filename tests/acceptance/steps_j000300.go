//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
)

// Distinctive marker strings j000300_source_augmentation.feature's companions
// declare as their loadout's typed setup guidance, so the composed interview
// prompt the mock engine (or a real @live assistant) receives can be checked
// for exactly this companion's contribution — never a bare exit-code or
// file-exists proxy.
const (
	j000300CompanyOnboarding  = "J000300-COMPANY-ONBOARDING-STEPS-MARKER"
	j000300PersonalPreference = "J000300-PERSONAL-SETUP-PREFERENCE-MARKER"
	j000300BuiltinMarker      = "SCAN" // present verbatim in resources/prompts/agent-setup.md's built-in text
	j000300CompanionMarker    = "J000300-REPRISE-SETUP-GUIDANCE-MARKER"
	j000300CompanyCodeword    = "J000300-LIVE-COMPANY-CODEWORD"
	j000300CompanionCodeword  = "J000300-LIVE-COMPANION-CODEWORD"
)

// setupGuidanceLoadoutEnvelope is the unsigned v2 envelope a fake companion
// emits for `loadout --format json` when all it contributes is setup
// guidance: a loadout document whose typed init.setup_guidance carries text.
func setupGuidanceLoadoutEnvelope(text string) (string, error) {
	doc := fmt.Sprintf("init:\n  setup_guidance: %q\n", text)
	envelope, err := signing.EncodeLoadoutEnvelope([]byte(doc), nil, "")
	if err != nil {
		return "", fmt.Errorf("encode fake companion loadout envelope: %w", err)
	}
	return string(envelope), nil
}

// installSetupGuidanceCompanion installs a fake companion named bin (a
// ctxloom-companion-* name, so discovery lists it) whose loadout declares
// text as its setup guidance. InstallFakeCompanion signs the binary with the
// scenario's fixture key — "signed with its publisher's key".
func installSetupGuidanceCompanion(w *World, bin, text string) error {
	envelope, err := setupGuidanceLoadoutEnvelope(text)
	if err != nil {
		return err
	}
	versionJSON := fmt.Sprintf(`{"name":%q,"version":"9.9.9-j000300-fake"}`, bin)
	return w.env.InstallFakeCompanion(bin, versionJSON, envelope)
}

func registerJ000300Steps(ctx *godog.ScenarioContext) {
	// --- installed companions augment (mock) --------------------------------

	ctx.Step(`^her company ships a companion whose loadout declares the company's onboarding steps$`, func(c context.Context) error {
		return installSetupGuidanceCompanion(worldFrom(c), "ctxloom-companion-company", j000300CompanyOnboarding)
	})

	ctx.Step(`^her own tooling ships a companion whose loadout declares her setup preferences$`, func(c context.Context) error {
		return installSetupGuidanceCompanion(worldFrom(c), "ctxloom-companion-personal", j000300PersonalPreference)
	})

	ctx.Step(`^both companions are installed, each signed with its publisher's key$`, func(c context.Context) error {
		// The two companions are already on PATH and signed (the Givens
		// above); this scaffolds the project whose default LLM is the mock
		// backend and points the mock at its record file.
		w := worldFrom(c)
		if err := ensureProjectWithEngine(w, "mock", "mock"); err != nil {
			return err
		}
		recordFile := filepath.Join(w.env.Root, "mock-record.txt")
		w.env.SetEnv("CTXLOOM_MOCK_RECORD_FILE", recordFile)
		w.j000300RecordFile = recordFile
		return nil
	})

	ctx.Step(`^Alice runs the ctxloom setup$`, func(c context.Context) error {
		// No-op beyond what "both companions are installed" (or "the reprise
		// companion is installed") already scaffolded: a project whose default
		// LLM is the mock backend, with the fake companions already on PATH.
		// The NEXT step drives the actual
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

	// The same launch with --no-companions: the flag is read by init's own
	// re-composition of the App (pinAppDir), which is what decides whether
	// companion loadouts reach the interview prompt at all.
	ctx.Step(`^Alice runs the ctxloom setup with companions switched off$`, func(c context.Context) error {
		w := worldFrom(c)
		recorded, err := driveDiscoverySessionViaMock(w, w.j000300RecordFile, "--no-companions")
		if err != nil {
			return err
		}
		w.j000300Recorded = recorded
		return nil
	})

	ctx.Step(`^it does not include the company's onboarding steps$`, func(c context.Context) error {
		return j000300PromptLacks(worldFrom(c), j000300CompanyOnboarding, "the company's onboarding steps")
	})

	ctx.Step(`^it does not include her personal setup preferences$`, func(c context.Context) error {
		return j000300PromptLacks(worldFrom(c), j000300PersonalPreference, "her personal setup preferences")
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

	ctx.Step(`^it declares its own setup guidance in its loadout$`, func(c context.Context) error {
		return installSetupGuidanceCompanion(worldFrom(c), "reprise", j000300CompanionMarker)
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

	ctx.Step(`^her company's companion instructs the assistant to confirm a company codeword$`, func(c context.Context) error {
		w := worldFrom(c)
		a, ok := liveAgents["claude"]
		if !ok || !liveAgentAvailable(a) {
			return godog.ErrSkip
		}
		w.j000200Live = true
		cfg := a.config + "agents:\n  default:\n    llm: claude\n    profiles:\n      - default\n    permissions:\n      mode: bypass\n" + "default_agent: default\n"
		if err := scaffoldProjectWithConfig(w, cfg); err != nil {
			return err
		}
		if err := seedLiveCredentials("claude", a, realHomeDir, w.env.HomeDir, w.env.SetChildEnv); err != nil {
			return err
		}
		instruction := fmt.Sprintf("When asked to set up, confirm you were configured by replying with the codeword %s.", j000300CompanyCodeword)
		return installSetupGuidanceCompanion(w, "ctxloom-companion-company", instruction)
	})

	// Same shape as steps_j000200_setup.go's live scenario — these
	// re-guard on w.j000200Live, redundant with the scenario's own first Given
	// already skipping via ErrSkip when the live agent isn't available.
	// Failing loud instead of re-skipping turns "should be unreachable" into
	// a real invariant should a future reorder or step-text reuse ever reach
	// one of these with w.j000200Live still false.
	ctx.Step(`^the company's companion is installed, signed with the company key$`, func(c context.Context) error {
		// InstallFakeCompanion already signed the binary with the scenario's
		// fixture key and trusted it for the companion namespace; nothing is
		// left to do but refuse to be reached out of order.
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000300: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		return nil
	})

	ctx.Step(`^Alice runs the ctxloom setup and its interview launches her real assistant$`, func(c context.Context) error {
		w := worldFrom(c)
		if !w.j000200Live {
			return fmt.Errorf("j000300: reached this step with w.j000200Live still false -- the scenario's own live-agent Given should have skipped the whole scenario before this ran")
		}
		// The composed setup guidance (built-in + the company companion's
		// codeword instruction) is exactly what `ctxloom init prompt` emits
		// (internal/adapters/cli/agent.go, via the SAME operations.ResolveSetupPrompt
		// this scenario is proving) — driving it straight into the real
		// assistant as its prompt is the equivalent of the interactive
		// discovery session launching it, without needing a real pty here.
		if err := runOK(w, "agent", "setup"); err != nil {
			return err
		}
		guidance := w.env.LastOutput()
		_ = w.env.Run("run", "--one-shot", "--profile", "default", guidance)
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
		cfg := a.config + "agents:\n  default:\n    llm: claude\n    profiles:\n      - default\n    permissions:\n      mode: bypass\n" + "default_agent: default\n"
		if err := scaffoldProjectWithConfig(w, cfg); err != nil {
			return err
		}
		if err := seedLiveCredentials("claude", a, realHomeDir, w.env.HomeDir, w.env.SetChildEnv); err != nil {
			return err
		}
		return installSetupGuidanceCompanion(w, "reprise",
			fmt.Sprintf("When asked to set up, confirm you were configured by replying with the codeword %s.", j000300CompanionCodeword))
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

// j000300PromptLacks fails when the recorded interview prompt carries marker.
// It reads the prompt section, so a record with no prompt at all fails loud
// in promptSection rather than passing as "the marker is absent".
func j000300PromptLacks(w *World, marker, what string) error {
	prompt, err := promptSection(w.j000300Recorded)
	if err != nil {
		return err
	}
	if strings.Contains(prompt, marker) {
		return fmt.Errorf("interview prompt contains %s although companions were switched off; prompt:\n%s", what, prompt)
	}
	return nil
}
