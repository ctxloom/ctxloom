//go:build integration || acceptance

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
)

// InstallFakeCompanion writes an executable shell script named bin (e.g.
// "reprise") into a fresh directory prepended to this PROCESS's PATH — so
// every subprocess this environment spawns (env.Run/RunPTY/Command, all of
// which build their env from os.Environ() at call time) finds it exactly as
// it would find a real companion binary on the developer's machine.
// versionJSON/loadoutJSON are the literal stdout this fake emits for
// `<bin> version --format json` / `<bin> loadout --format json` — the two
// companion-loadout-protocol subcommands the companion probes exec (see
// internal/adapters/companions). The fake is then registered (RegisterCompanion).
//
// The PATH change is applied via storeAndSetEnv, the SAME mechanism Setup
// uses for HOME/XDG — so TestEnvironment.Cleanup restores the original PATH
// when the scenario tears down, and this fake binary never leaks into a
// later scenario in the same test process.
//
// Calling this more than once in one scenario (e.g. faking BOTH ltk and
// reprise together, as J001800's guardrails journey does) installs each fake
// independently: the env var names are namespaced per bin (companionEnvVar),
// not shared globals, so a second call cannot clobber the first fake's
// payload out from under it — a real bug this fix replaces (a shared
// COMPANION_VERSION_JSON/COMPANION_LOADOUT pair meant the
// most-recently-installed companion silently overwrote every previously
// installed one's script into echoing ITS OWN content instead).
func (e *TestEnvironment) InstallFakeCompanion(bin, versionJSON, loadoutDoc string) error {
	if _, err := e.PlaceFakeCompanion(bin, versionJSON, loadoutDoc); err != nil {
		return err
	}
	// REGISTRATION. A companion is executed only when its name is registered,
	// so installing the binary is not enough to make it contribute anything.
	// Register it the way a human does, in THIS scenario's HOME, so what these
	// scenarios exercise is the production path rather than a bypass. A real
	// ltk on the developer's own PATH is registered nowhere in this HOME, so
	// it is never run — a scenario's result cannot depend on what the machine
	// happens to have.
	name, err := companionNameOf(bin)
	if err != nil {
		return err
	}
	return e.RegisterCompanion(name)
}

// PlaceFakeCompanion is InstallFakeCompanion without the registration: the
// fake is on PATH and answers like a companion, and nothing registered it —
// what a binary an npm dependency dropped on PATH looks like to ctxloom. It
// returns the directory the fake was written to.
func (e *TestEnvironment) PlaceFakeCompanion(bin, versionJSON, loadoutDoc string) (string, error) {
	dir, err := os.MkdirTemp(e.Root, "fake-companion-*")
	if err != nil {
		return "", fmt.Errorf("create fake companion dir: %w", err)
	}
	versionVar := companionEnvVar("COMPANION_VERSION_JSON", bin)
	loadoutVar := companionEnvVar("COMPANION_LOADOUT", bin)
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  version) printf '%%s' "$%s" ;;
  loadout) printf '%%s' "$%s" ;;
  *) exit 1 ;;
esac
`, versionVar, loadoutVar)
	path := filepath.Join(dir, bin)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", fmt.Errorf("write fake companion %q: %w", bin, err)
	}

	// The payloads are handed to the script via env (rather than inlined
	// into the script text) so a loadout document — arbitrary bytes — never
	// has to survive shell quoting.
	e.storeAndSetEnv(versionVar, versionJSON)
	e.storeAndSetEnv(loadoutVar, loadoutDoc)

	pathSep := string(os.PathListSeparator)
	current := os.Getenv("PATH")
	e.storeAndSetEnv("PATH", dir+pathSep+current)
	return dir, nil
}

// RegisterCompanion registers name in the scenario's HOME by running
// `ctxloom companion add <name>`: the binary must be on PATH and answer the
// loadout probe. Exported so a scenario that wants an UNREGISTERED companion
// on PATH simply does not call it.
func (e *TestEnvironment) RegisterCompanion(name string) error {
	out, err := e.Command(nil, "companion", "add", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("register companion %q: %w: %s", name, err, out)
	}
	return nil
}

// companionNameOf is the registration name a fake companion binary answers
// to: a first-party binary is its own name, any other is the name
// ctxloom-companion-<name> carries.
func companionNameOf(bin string) (string, error) {
	if companions.BinaryName(bin) == bin {
		return bin, nil
	}
	name := strings.TrimPrefix(bin, "ctxloom-companion-")
	if companions.BinaryName(name) != bin {
		return "", fmt.Errorf("fake companion %q is not named for any registration (want a first-party name or ctxloom-companion-<name>)", bin)
	}
	return name, nil
}

// companionEnvVar builds the per-bin env var name InstallFakeCompanion's fake
// script reads: prefix, an underscore, and bin uppercased with every
// non-alphanumeric byte (e.g. the "-" in "ctxloom-companion-foo") folded to
// "_" so the result is always a valid shell variable name.
func companionEnvVar(prefix, bin string) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteByte('_')
	for _, r := range strings.ToUpper(bin) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
