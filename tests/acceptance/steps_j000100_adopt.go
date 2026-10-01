//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
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

// ctxloomHookLines is every hook in rel's hooks table, across events, as
// its argv read as one line (j000400HookCommandsFrom), that runs a ctxloom
// hook verb. A whole-file substring cannot answer this: ctxloom's own hooks
// are exec form, the executable and the verb separate JSON strings, and the
// statusline (`ctxloom hook hud`) is not a hook at all.
func ctxloomHookLines(w *World, rel string) ([]string, error) {
	doc, err := j000400ReadJSON(w, rel)
	if err != nil {
		return nil, err
	}
	top, _ := doc["hooks"].(map[string]any)
	var out []string
	for _, event := range top {
		for _, line := range j000400HookCommandsFrom(event) {
			if strings.HasPrefix(line, "ctxloom hook ") {
				out = append(out, line)
			}
		}
	}
	return out, nil
}

func registerJ000100Steps(ctx *godog.ScenarioContext) {
	// The wired project as a PRECONDITION, for the scenarios where installing
	// is the starting position rather than the subject. Keeping it out of the
	// narrated command form is what lets a reader — and the generated page —
	// see which command each scenario is about, and keeps a defect in install
	// failing the scenario that tests install rather than every scenario that
	// needs a wired project.
	//
	// It runs the real commands rather than writing the files they produce:
	// a fabricated installed state drifts from the real one, and every scenario
	// resting on it would then assert against a state the product never
	// reaches. "Wired" is the scaffold PLUS the explicit project-side hooks
	// install — `manage install` alone writes no engine file (a `ctxloom run`
	// session carries its own surfaces).
	ctx.Step(`^ctxloom is already wired into her project$`, func(c context.Context) error {
		for _, cmdline := range []string{
			"ctxloom manage install --engine claude-code",
			"ctxloom manage hooks install",
		} {
			if err := runCLI(c, cmdline, ""); err != nil {
				return err
			}
			// A precondition checks its own exit: a silent failure here makes
			// every assertion after it meaningless, and the scenario has no
			// reason to write a Then for setup.
			if code := worldFrom(c).env.LastExitCode(); code != 0 {
				return fmt.Errorf("precondition `%s` failed (exit %d):\n%s",
					cmdline, code, worldFrom(c).env.LastOutput())
			}
		}
		return nil
	})

	ctx.Step(`^her assistant is wired to receive the project's context at the start of every session$`,
		func(c context.Context) error {
			return assertSessionStartHookCommand(worldFrom(c), ".claude/settings.json", "hook inject-context", true)
		})

	// Uninstall is the empty plan over the ownership record: a settings.json
	// ctxloom CREATED in this project leaves with it, and an ABSENT file is
	// the stronger form of "nothing left behind". A file that still stands
	// is one Alice authored, and then it must carry none of ctxloom's
	// entries.
	ctx.Step(`^nothing ctxloom wired into her assistant is left behind$`,
		func(c context.Context) error {
			w := worldFrom(c)
			if !w.env.FileExists(".claude/settings.json") {
				return nil
			}
			body, err := w.env.ReadFile(".claude/settings.json")
			if err != nil {
				return err
			}
			// Both halves, named separately: the hooks table parsed (an exec
			// hook's verb is not in its command string), and the statusline.
			left, err := ctxloomHookLines(w, ".claude/settings.json")
			if err != nil {
				return err
			}
			if len(left) > 0 {
				return fmt.Errorf("ctxloom hooks survived uninstall: %v", left)
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

	// The rules live in the NESTED .ctxloom/.gitignore ctxloom owns, not in the
	// project's own file: ctxloom does not write the project's .gitignore, so
	// asserting there would assert a file the product never produces.
	ctx.Step(`^ctxloom's own working state is kept out of source control$`, func(c context.Context) error {
		body, err := worldFrom(c).env.ReadFile(".ctxloom/.gitignore")
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), "ctxloom") {
			return fmt.Errorf(".ctxloom/.gitignore does not exclude any ctxloom state:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^ctxloom reports the engine it wired her project for$`, func(c context.Context) error {
		out := worldFrom(c).env.LastStdout()
		if !strings.Contains(out, "claude-code") {
			return fmt.Errorf("manage check never named the engine it wired; stdout:\n%s", out)
		}
		return nil
	})

	// Asserted on the shared DOCTOR-CHECK-* marker vocabulary rather than a
	// human-readable summary line, so a rewording does not silently change what
	// this proves. Naming several markers is the point: one marker is satisfied
	// by a doctor that runs a single check and stops.
	ctx.Step(`^she is shown a health check for every part of the harness$`, func(c context.Context) error {
		out := worldFrom(c).env.LastStdout()
		for _, marker := range []string{
			"DOCTOR-CHECK-DEPS-a1",
			"DOCTOR-CHECK-AGENTS-b2",
			"DOCTOR-CHECK-HOOKS-TRUST-d4",
		} {
			if !strings.Contains(out, marker) {
				return fmt.Errorf("doctor did not report %s on stdout:\n%s", marker, out)
			}
		}
		return nil
	})
}
