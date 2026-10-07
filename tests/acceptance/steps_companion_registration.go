//go:build acceptance

// Companion REGISTRATION (cli/companion.feature): a binary that merely SITS on
// $PATH under a companion name is never something ctxloom runs; only a name
// registered with `ctxloom companion add` is.
//
// The assertion that matters here is not "the command exited 0" — it is WHICH
// BINARIES ACTUALLY RAN. The fake companion these steps install appends a line
// to a witness file every time it is invoked, so "was never executed" is read
// off the filesystem rather than inferred from a missing warning or an empty
// output section.
//
// These steps deliberately do NOT use testenv.InstallFakeCompanion, which
// registers as part of installing: here registration is the subject, so the
// fixture stops at "on PATH" and the scenario registers through the CLI.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// companionWitnessName is the file the fake companion appends to when it runs.
const companionWitnessName = "companion-exec-witness.txt"

func registerCompanionRegistrationSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a companion "([^"]*)" is on PATH, not registered$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		dir, err := os.MkdirTemp(w.env.Root, "unregistered-companion-*")
		if err != nil {
			return fmt.Errorf("create companion dir: %w", err)
		}
		return placeWitnessCompanion(w, dir, bin)
	})

	// The npm case: a transitive dependency ships a ctxloom-companion-* binary
	// into node_modules/.bin, and npx/direnv put that directory on PATH as an
	// ABSOLUTE entry (exec.LookPath already refuses a relative one).
	ctx.Step(`^a dependency drops a companion "([^"]*)" into an absolute node_modules/\.bin on PATH$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		dir := filepath.Join(w.env.Root, "web-app", "node_modules", ".bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create node_modules/.bin: %w", err)
		}
		return placeWitnessCompanion(w, dir, bin)
	})

	// Forget the invocations so far, so a later "was executed" is about what
	// happened after this point (registering a companion runs its probe).
	ctx.Step(`^the companion executions so far are forgotten$`, func(c context.Context) error {
		err := os.Remove(filepath.Join(worldFrom(c).env.Root, companionWitnessName))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})

	ctx.Step(`^the home config registers the companion "([^"]*)" by name only$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		data, err := os.ReadFile(paths.ConfigPath(filepath.Join(w.env.HomeDir, paths.AppDirName))) //nolint:gosec // the scenario's own home config
		if err != nil {
			return fmt.Errorf("read the home config: %w", err)
		}
		cfg, err := config.ParseConfig(data)
		if err != nil {
			return fmt.Errorf("parse the home config: %w", err)
		}
		got := cfg.GetCompanions()
		if len(got) != 1 || got[0] != name {
			return fmt.Errorf("home config registers %v, want exactly [%s]:\n%s", got, name, data)
		}
		if strings.Contains(string(data), w.env.Root) {
			return fmt.Errorf("the home config records a path; a registration is the name only:\n%s", data)
		}
		return nil
	})

	// A companion that SHIPS A HOOK, for scenarios about where a hook came
	// from. It exists because those scenarios used to read the developer's own
	// ltk/taskloom/reprise off $PATH: the assertion "a companion-sourced hook
	// is present" was satisfied by the machine, not by the fixture. The
	// scenario now installs (and registers) the companion whose hook it
	// asserts on.
	ctx.Step(`^a companion "([^"]*)" shipping a "([^"]*)" hook is on PATH$`, func(c context.Context, bin, event string) error {
		w := worldFrom(c)
		bundle := fmt.Sprintf(`name: %s
version: "1.0"
hooks:
  %s:
    - type: command
      command: echo HOOK-FROM-COMPANION-%s
`, bin, event, bin)
		version := fmt.Sprintf(`{"name":%q,"version":"0.0.0-fixture"}`, bin)
		return j001800InstallFakeCompanion(w, bin, string(testsupport.RunLoadout(bundle)), version)
	})

	ctx.Step(`^the companion "([^"]*)" was never executed$`, func(c context.Context, bin string) error {
		ran, err := companionExecCount(worldFrom(c), bin)
		if err != nil {
			return err
		}
		if ran != 0 {
			return fmt.Errorf("companion %q was executed %d time(s); it is not registered and must not have run", bin, ran)
		}
		return nil
	})

	ctx.Step(`^the companion "([^"]*)" was executed$`, func(c context.Context, bin string) error {
		ran, err := companionExecCount(worldFrom(c), bin)
		if err != nil {
			return err
		}
		if ran == 0 {
			return fmt.Errorf("companion %q was never executed, but it is registered", bin)
		}
		return nil
	})
}

// placeWitnessCompanion writes an executable fake named bin into dir and
// prepends dir to $PATH. It records nothing: ctxloom meets it exactly as it
// would meet a binary an npm dependency dropped into node_modules/.bin.
//
// The script witnesses its own invocation FIRST, before answering anything, so
// even a probe whose output ctxloom discards still leaves a mark. It answers
// `loadout` and `version` with valid shapes, so once registered it runs
// cleanly — the difference between the scenarios is the registration alone.
func placeWitnessCompanion(w *World, dir, bin string) error {
	witness := filepath.Join(w.env.Root, companionWitnessName)
	loadout := filepath.Join(dir, bin+".loadout")
	if err := os.WriteFile(loadout, testsupport.RunLoadout("version: \"1.0\"\n"), 0o644); err != nil { //nolint:gosec // a fixture document
		return fmt.Errorf("write fake companion loadout %q: %w", bin, err)
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%s %%s\n' "$1" >> %q
case "$1" in
  loadout) cat %q ;;
  version) printf '{"name":"%s","version":"0.0.0-fixture"}' ;;
  *) exit 1 ;;
esac
`, bin, witness, loadout, bin)
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755); err != nil { //nolint:gosec // a fake companion must be executable
		return fmt.Errorf("write fake companion %q: %w", bin, err)
	}
	w.env.SetEnv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return nil
}

// companionExecCount reports how many times bin recorded an invocation. An
// absent witness file means zero runs — the file is only ever created by the
// fake itself.
func companionExecCount(w *World, bin string) (int, error) {
	data, err := os.ReadFile(filepath.Join(w.env.Root, companionWitnessName))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read companion exec witness: %w", err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, bin+" ") {
			count++
		}
	}
	return count, nil
}
