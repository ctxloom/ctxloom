//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

// Package acceptance: J000100's business vocabulary.
//
// Every step here is a thin translation onto the same world helpers the
// mechanical steps use — it reads a file through w.env, or the last command's
// output, and nothing else. A journey step gets no private fixture; state it
// needs, it builds the way a CLI spec builds it.
//
// That is what makes one vocabulary at two granularities hold. A journey says
// "her assistant is wired to receive the project's context"; cli/manage.feature
// says `.claude/settings.json` carries the inject-context hook. Same underlying
// read, different altitude — so the two files cannot drift into disagreeing
// about what WIRED means.

// sessionStartHookRe matches settings.json only when a SessionStart entry
// carries the inject-context command as its own command string.
//
// A substring cannot do this job: ctxloom writes two things into settings.json
// whose text overlaps. `ctxloom hook` alone matches the statusline entry
// (`<abs>/ctxloom hook hud`), while the context hook's command is
// `'<abs>/ctxloom' hook inject-context ...` — quoted between the two words.
// Requiring the SessionStart key, a command field, and the inject-context leaf
// together is what ties the match to the hook that actually delivers context.
var sessionStartHookRe = regexp.MustCompile(`(?s)"SessionStart".*"command": "[^"]*' hook inject-context`)

func registerJ000100Steps(ctx *godog.ScenarioContext) {
	// The wired project as a PRECONDITION, for the scenarios where installing
	// is the starting position rather than the subject. Keeping it out of the
	// narrated command form is what lets a reader — and the generated page —
	// see which command each scenario is about, and keeps a defect in install
	// failing the scenario that tests install rather than every scenario that
	// needs a wired project.
	//
	// It runs the real command rather than writing the files install produces:
	// a fabricated installed state drifts from the real one, and every scenario
	// resting on it would then assert against a state the product never
	// reaches.
	ctx.Step(`^ctxloom is already wired into her project$`, func(c context.Context) error {
		if err := runCLI(c, "ctxloom manage install --engine claude-code", ""); err != nil {
			return err
		}
		// A precondition checks its own exit: a silent failure here makes every
		// assertion after it meaningless, and the scenario has no reason to
		// write a Then for setup.
		if code := worldFrom(c).env.LastExitCode(); code != 0 {
			return fmt.Errorf("precondition `manage install` failed (exit %d):\n%s",
				code, worldFrom(c).env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^her assistant is wired to receive the project's context at the start of every session$`,
		func(c context.Context) error {
			body, err := worldFrom(c).env.ReadFile(".claude/settings.json")
			if err != nil {
				return err
			}
			if !sessionStartHookRe.MatchString(string(body)) {
				return fmt.Errorf("no SessionStart hook delivering context in .claude/settings.json:\n%s", body)
			}
			return nil
		})

	ctx.Step(`^nothing ctxloom wired into her assistant is left behind$`,
		func(c context.Context) error {
			body, err := worldFrom(c).env.ReadFile(".claude/settings.json")
			if err != nil {
				return err
			}
			// Both halves, named separately. `ctxloom hook` alone matches only
			// the statusline, so on its own it would report a clean uninstall
			// while the context hook was still in place.
			if sessionStartHookRe.MatchString(string(body)) {
				return fmt.Errorf("the SessionStart context hook survived uninstall:\n%s", body)
			}
			if strings.Contains(string(body), "ctxloom hook") {
				return fmt.Errorf("a ctxloom hook command survived uninstall:\n%s", body)
			}
			return nil
		})

	// "Still hers" is the scaffold surviving, which is what makes uninstall
	// reversible rather than destructive: ctxloom removes what it wired into
	// the ENGINE and leaves the project's own content — the bundles, profiles
	// and config a team authored — where it is.
	ctx.Step(`^the context she authored is still hers$`, func(c context.Context) error {
		if _, err := worldFrom(c).env.ReadFile(".ctxloom/config.yaml"); err != nil {
			return fmt.Errorf("uninstall took the project's own ctxloom content with it: %w", err)
		}
		return nil
	})

	ctx.Step(`^ctxloom's own working state is kept out of source control$`, func(c context.Context) error {
		body, err := worldFrom(c).env.ReadFile(".gitignore")
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), "ctxloom") {
			return fmt.Errorf(".gitignore does not exclude any ctxloom state:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^ctxloom reports the engine it wired her project for$`, func(c context.Context) error {
		out := worldFrom(c).env.LastOutput()
		if !strings.Contains(out, "claude-code") {
			return fmt.Errorf("manage check never named the engine it wired:\n%s", out)
		}
		return nil
	})

	// Asserted on the shared DOCTOR-CHECK-* marker vocabulary rather than a
	// human-readable summary line, so a rewording does not silently change what
	// this proves. Naming several markers is the point: one marker is satisfied
	// by a doctor that runs a single check and stops.
	ctx.Step(`^she is shown a health check for every part of the harness$`, func(c context.Context) error {
		out := worldFrom(c).env.LastOutput()
		for _, marker := range []string{
			"DOCTOR-CHECK-DEPS-a1",
			"DOCTOR-CHECK-AGENTS-b2",
			"DOCTOR-CHECK-HOOKS-TRUST-d4",
		} {
			if !strings.Contains(out, marker) {
				return fmt.Errorf("doctor did not report %s:\n%s", marker, out)
			}
		}
		return nil
	})
}
