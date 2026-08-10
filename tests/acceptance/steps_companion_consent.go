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
	"io"
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

	// The first-party PROVENANCE fixture. See installFirstPartyBesideCtxloom
	// for why co-location is the only way to reproduce the exemption.
	ctx.Step(`^ctxloom is installed beside a first-party companion "([^"]*)"$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		return installFirstPartyBesideCtxloom(w, bin)
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
	if err := writeFakeCompanion(w, dir, bin); err != nil {
		return err
	}
	prependPATH(w, dir)
	return nil
}

// writeFakeCompanion writes the witnessing fake named bin into dir. Split out
// of installUnconfirmedCompanion so the first-party fixture below installs the
// IDENTICAL binary: the only thing that differs between the two scenarios is
// WHERE it resolves from, which is the entire content of the provenance rule.
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

// installFirstPartyBesideCtxloom reproduces the ONE place in the trust model
// where a missing record does not deny: a shipped companion (ltk, taskloom,
// reprise) resolving from the directory the running ctxloom binary itself
// lives in is executed with no consent record at all (docs/trust-model.md,
// "First-party companions are exempt, but pinned by location").
//
// It copies the ctxloom under test into a fresh directory, writes the fake
// companion BESIDE it, prepends that directory to $PATH, and repoints the
// environment at the copy — so config.companionInstallDir (which is
// os.Executable's own directory, by construction) and the companion's
// resolved path are the same directory.
//
// The co-location is unavoidable, and that is the point of the step existing
// at all: the exemption is pinned to where the RUNNING binary lives precisely
// so that nothing an env var, a config key or a recorded install path can say
// will move it. Reproducing it therefore means moving the binary, which is
// what this does. No consent is granted anywhere here: if the exemption
// stopped working, the fake would be refused as unconfirmed and the scenario
// would go red rather than quietly proving nothing.
func installFirstPartyBesideCtxloom(w *World, bin string) error {
	dir, err := os.MkdirTemp(w.env.Root, "install-dir-*")
	if err != nil {
		return fmt.Errorf("create fake install dir: %w", err)
	}
	copied := filepath.Join(dir, "ctxloom")
	if err := copyExecutable(w.env.AppBinary, copied); err != nil {
		return err
	}
	if err := writeFakeCompanion(w, dir, bin); err != nil {
		return err
	}
	prependPATH(w, dir)
	// Every later `I run "ctxloom ..."` in this scenario runs the COPY, which
	// is what makes its own directory the install directory.
	w.env.AppBinary = copied
	return nil
}

// copyExecutable copies src to dst with the executable bit set. A copy rather
// than a symlink: config.companionInstallDir resolves symlinks before taking
// the directory, so a symlinked ctxloom would report the directory it points
// AT — the real build output — and the companion beside the link would not
// match it.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // the ctxloom binary under test
	if err != nil {
		return fmt.Errorf("open ctxloom binary %q: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) //nolint:gosec // it must be executable
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy ctxloom binary to %q: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %q: %w", dst, err)
	}
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
