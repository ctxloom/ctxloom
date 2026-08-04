//go:build acceptance

// Companion EXEC consent (trust_cli.feature): a binary that merely SITS on
// $PATH under a companion name is not yet something ctxloom will run.
//
// The assertion that matters here is not "the command exited 0" — it is WHICH
// BINARIES ACTUALLY RAN. The fake companion these steps install appends a line
// to a witness file every time it is invoked, so "was never executed" is read
// off the filesystem rather than inferred from a missing warning or an empty
// output section. ctxloom's characteristic bug is the silent no-op, and a
// consent gate is exactly the kind of change that can pass every exit-code
// assertion while quietly doing nothing (or quietly doing everything).
//
// These steps deliberately do NOT use testenv.InstallFakeCompanion: that
// helper grants consent as part of installing, because every OTHER journey's
// point is what a companion CONTRIBUTES, not whether it was allowed to run.
// Here the refusal is the subject, so the fixture stops at "on PATH".
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// companionWitnessName is the file the fake companion appends to when it runs.
const companionWitnessName = "companion-exec-witness.txt"

func registerCompanionConsentSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a discovered companion "([^"]*)" is on PATH, never confirmed$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		return installUnconfirmedCompanion(w, bin)
	})

	ctx.Step(`^the companion "([^"]*)" was never executed$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		ran, err := companionExecCount(w, bin)
		if err != nil {
			return err
		}
		if ran != 0 {
			return fmt.Errorf("companion %q was executed %d time(s); it was never confirmed and must not have run", bin, ran)
		}
		return nil
	})

	ctx.Step(`^the companion "([^"]*)" was executed$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		ran, err := companionExecCount(w, bin)
		if err != nil {
			return err
		}
		if ran == 0 {
			return fmt.Errorf("companion %q was never executed, but consent for it was recorded", bin)
		}
		return nil
	})
}

// installUnconfirmedCompanion writes an executable fake named bin into a fresh
// directory prepended to $PATH — the same reachability testenv.
// InstallFakeCompanion produces — and stops there: no consent is recorded, so
// ctxloom meets it for the first time exactly as it would meet a binary an npm
// dependency dropped into ./node_modules/.bin.
//
// The script witnesses its own invocation FIRST, before answering anything, so
// even a probe whose output ctxloom discards still leaves a mark. It answers
// `version` and `loadout` with the empty-but-valid shapes so a companion that
// IS allowed through contributes without erroring — the difference between the
// two scenarios is then consent alone, not a broken fixture.
func installUnconfirmedCompanion(w *World, bin string) error {
	dir, err := os.MkdirTemp(w.env.Root, "unconfirmed-companion-*")
	if err != nil {
		return fmt.Errorf("create unconfirmed companion dir: %w", err)
	}
	witness := filepath.Join(w.env.Root, companionWitnessName)
	script := fmt.Sprintf(`#!/bin/sh
printf '%s %%s\n' "$1" >> %q
case "$1" in
  version) printf '{"name":"%s","version":"0.0.0-unconfirmed"}' ;;
  *) exit 1 ;;
esac
`, bin, witness, bin)
	path := filepath.Join(dir, bin)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // a fake companion must be executable
		return fmt.Errorf("write unconfirmed companion %q: %w", bin, err)
	}
	pathSep := string(os.PathListSeparator)
	w.env.SetEnv("PATH", dir+pathSep+os.Getenv("PATH"))
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
