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
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// companionWitnessName is the file the fake companion appends to when it runs.
const companionWitnessName = "companion-exec-witness.txt"

func registerCompanionConsentSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a discovered companion "([^"]*)" is on PATH, unsigned$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		return installUnsignedCompanion(w, bin)
	})

	// Signed by a publisher the SCENARIO trusts: the fixture form of a
	// publisher vouching for the bytes, which is the only thing that admits a
	// companion.
	ctx.Step(`^the companion "([^"]*)" is signed by a publisher this project trusts$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		return w.env.SignCompanion(companionPath(w, bin))
	})

	// Signed by a well-formed key the trust root does not carry. Distinct from
	// unsigned on purpose: the two are different refusals and a reader who
	// cannot tell them apart cannot tell "nobody vouched" from "someone I do
	// not know vouched".
	ctx.Step(`^the companion "([^"]*)" is signed by a key this project does not trust$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		return signCompanionWithUntrustedKey(w, companionPath(w, bin))
	})

	// Signed, then EDITED. The signature is intact and its signer is trusted;
	// it simply no longer covers these bytes. Never degraded to "unsigned" — a
	// broken signature is a signal, not an absence.
	ctx.Step(`^the companion "([^"]*)" is edited after it was signed$`, func(c context.Context, bin string) error {
		w := worldFrom(c)
		path := companionPath(w, bin)
		if err := w.env.SignCompanion(path); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o755) //nolint:gosec // a fixture binary this step just wrote
		if err != nil {
			return fmt.Errorf("open %q to edit it: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		_, err = f.WriteString("\n# edited after signing\n")
		return err
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
func installUnsignedCompanion(w *World, bin string) error {
	dir, err := os.MkdirTemp(w.env.Root, "unsigned-companion-*")
	if err != nil {
		return fmt.Errorf("create companion dir: %w", err)
	}
	if err := writeFakeCompanion(w, dir, bin); err != nil {
		return err
	}
	prependPATH(w, dir)
	// Remembered so a LATER step can vouch for this exact file. A step that
	// re-derived the path would have to agree with this one about the temp
	// directory, and the two would drift the first time either changed.
	companionDirs[bin] = dir
	return nil
}

// companionDirs records where each fixture companion was installed, keyed by
// binary name, so the signing steps can name the file this suite actually
// wrote rather than searching $PATH for it.
var companionDirs = map[string]string{}

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

// signCompanionWithUntrustedKey signs path with a key minted here and trusted
// NOWHERE — deliberately not added to any allowed_signers.
//
// It exists so "untrusted signer" can be witnessed as its own outcome. Without
// it a scenario could only show unsigned-vs-signed, and the arm that refuses a
// real signature from a stranger would have no test at all.
func signCompanionWithUntrustedKey(w *World, path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("mint an untrusted key: %w", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		return fmt.Errorf("wrap the untrusted key: %w", err)
	}
	binary, err := os.ReadFile(path) //nolint:gosec // a fixture path this suite wrote
	if err != nil {
		return fmt.Errorf("read %q to sign it: %w", path, err)
	}
	statement := testsupport.CompanionReleaseStatement(filepath.Base(path), "1.0.0", binary)
	if err := os.WriteFile(path+".release", statement, 0o600); err != nil {
		return fmt.Errorf("write the release statement for %q: %w", path, err)
	}
	sig, err := signing.Sign(statement, signer, signing.NamespaceCompanion)
	if err != nil {
		return fmt.Errorf("sign %q: %w", path, err)
	}
	return os.WriteFile(path+".sig", sig, 0o600)
}
