//go:build acceptance

// Companion EXEC admission (cli/companion.feature): a binary that merely SITS
// on $PATH under a companion name is not yet something ctxloom will run.
//
// The assertion that matters here is not "the command exited 0" — it is WHICH
// BINARIES ACTUALLY RAN. The fake companion these steps install appends a line
// to a witness file every time it is invoked, so "was never executed" is read
// off the filesystem rather than inferred from a missing warning or an empty
// output section. ctxloom's characteristic bug is the silent no-op, and a
// admission gate is exactly the kind of change that can pass every exit-code
// assertion while quietly doing nothing (or quietly doing everything).
//
// These steps deliberately do NOT use testenv.InstallFakeCompanion: that
// helper records an allow as part of installing, because every OTHER journey's
// point is what a companion CONTRIBUTES, not whether it was allowed to run.
// Here the refusal is the subject, so the fixture stops at "on PATH".
package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// companionWitnessName is the file the fake companion appends to when it runs.
const companionWitnessName = "companion-exec-witness.txt"

func registerCompanionConsentSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a discovered companion "([^"]*)" is on PATH, not allowed$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		return installUnallowedCompanion(w, bin)
	})

	// Allowed the way `ctxloom companion allow --yes` records it, in the
	// scenario's own HOME.
	ctx.Step(`^the companion "([^"]*)" is allowed$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		return w.env.AllowCompanion(companionPath(w, bin))
	})

	// Allowed, then its bytes change — a rebuild. The path is still allowed;
	// the hash on record no longer matches. Both hashes are remembered so a
	// later step can check the disclosure names them.
	ctx.Step(`^the companion "([^"]*)" is allowed, then rebuilt$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		path := companionPath(w, bin)
		if err := w.env.AllowCompanion(path); err != nil {
			return err
		}
		allowed, err := fileSHA256(path)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o755) //nolint:gosec // a fixture binary this step just wrote
		if err != nil {
			return fmt.Errorf("open %q to rebuild it: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString("\n# rebuilt after it was allowed\n"); err != nil {
			return err
		}
		current, err := fileSHA256(path)
		if err != nil {
			return err
		}
		companionHashes[bin] = [2]string{allowed, current}
		return nil
	})

	ctx.Step(`^the output names the allowed and the current hash of "([^"]*)"$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		h, ok := companionHashes[bin]
		if !ok {
			return fmt.Errorf("no rebuild of %q was recorded by this scenario", bin)
		}
		want := h[0] + " -> " + h[1]
		if !strings.Contains(w.env.LastOutput(), want) {
			return fmt.Errorf("output does not name the hash change %q; output:\n%s", want, w.env.LastOutput())
		}
		return nil
	})

	// A companion that SHIPS A HOOK, for scenarios about where a hook came
	// from. It exists because those scenarios used to read the developer's own
	// ltk/taskloom/reprise off $PATH: the assertion "a companion-sourced hook
	// is present" was satisfied by the machine, not by the fixture, so it said
	// nothing on a box without them and shifted hook ordinals on a box with
	// them. The scenario now installs the companion whose hook it asserts on.
	ctx.Step(`^a signed companion "([^"]*)" shipping a "([^"]*)" hook is on PATH$`, func(c context.Context, bin, event string) error {
		w := worldFrom(c)
		bundle := fmt.Sprintf(`name: %s
version: "1.0"
hooks:
  %s:
    - type: command
      command: echo HOOK-FROM-COMPANION-%s
`, bin, event, bin)
		version := fmt.Sprintf(`{"name":%q,"version":"0.0.0-fixture"}`, bin)
		return j001800InstallFakeCompanion(w, bin, "fixture-companion@testenv.invalid", string(testsupport.RunLoadout(bundle)), version)
	})

	ctx.Step(`^the companion "([^"]*)" was never executed$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		ran, err := companionExecCount(w, bin)
		if err != nil {
			return err
		}
		if ran != 0 {
			return fmt.Errorf("companion %q was executed %d time(s); it was never allowed and must not have run", bin, ran)
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
			return fmt.Errorf("companion %q was never executed, but an allow for it was recorded", bin)
		}
		return nil
	})
}

// installUnallowedCompanion writes an executable fake named bin into a fresh
// directory prepended to $PATH — the same reachability testenv.
// InstallFakeCompanion produces — and stops there: no allow is recorded, so
// ctxloom meets it for the first time exactly as it would meet a binary an npm
// dependency dropped into ./node_modules/.bin.
//
// The script witnesses its own invocation FIRST, before answering anything, so
// even a probe whose output ctxloom discards still leaves a mark. It answers
// `version` with a valid shape so a companion that IS allowed through runs
// without erroring — the difference between the scenarios is then the allow
// alone, not a broken fixture.
func installUnallowedCompanion(w *World, bin string) error {
	dir, err := os.MkdirTemp(w.env.Root, "unallowed-companion-*")
	if err != nil {
		return fmt.Errorf("create companion dir: %w", err)
	}
	if err := writeFakeCompanion(w, dir, bin); err != nil {
		return err
	}
	prependPATH(w, dir)
	// Remembered so a LATER step can allow this exact file. A step that
	// re-derived the path would have to agree with this one about the temp
	// directory, and the two would drift the first time either changed.
	companionDirs[bin] = dir
	return nil
}

// companionDirs records where each fixture companion was installed, keyed by
// binary name, so the allow steps can name the file this suite actually wrote
// rather than searching $PATH for it.
var companionDirs = map[string]string{}

// companionHashes records, per binary, the hash it was allowed at and the hash
// it was rebuilt to.
var companionHashes = map[string][2]string{}

// writeFakeCompanion writes the witnessing fake named bin into dir.
func writeFakeCompanion(w *World, dir, bin string) error {
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
		return fmt.Errorf("write fake companion %q: %w", bin, err)
	}
	return nil
}

// prependPATH puts dir ahead of everything else on $PATH, so a fake shadows
// any same-named binary this developer really has installed.
func prependPATH(w *World, dir string) {
	w.env.SetEnv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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

// companionPath is where a fixture companion named bin was installed.
func companionPath(_ *World, bin string) string {
	return filepath.Join(companionDirs[bin], bin)
}

// fileSHA256 is the lowercase hex SHA-256 of the file at path — the hash an
// allow record holds.
func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // a fixture path this suite wrote
	if err != nil {
		return "", fmt.Errorf("hash %q: %w", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// forgetCompanionAllow withdraws the allow for the binary at path from the
// scenario's HOME, so ctxloom refuses it.
func forgetCompanionAllow(w *World, path string) error {
	key, err := companions.ResolveCompanion(path)
	if err != nil {
		return err
	}
	store := companions.NewAllowStoreAt(afero.NewOsFs(),
		filepath.Join(w.env.HomeDir, paths.AppDirName, paths.CompanionAllowFileName+".yaml"))
	_, err = store.Forget(key)
	return err
}
