//go:build integration || acceptance

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
)

// InstallFakeCompanion writes an executable shell script named bin (e.g.
// "reprise") into a fresh directory prepended to this PROCESS's PATH — so
// every subprocess this environment spawns (env.Run/RunPTY/Command, all of
// which build their env from os.Environ() at call time) finds it exactly as
// it would find a real companion binary on the developer's machine.
// versionJSON/loadoutJSON are the literal stdout this fake emits for
// `<bin> version --format json` / `<bin> loadout --format json` — the two
// companion-loadout-protocol subcommands config.DiscoverCompanions/
// ProbeCompanionLoadouts actually exec (see internal/config/companions.go).
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
// COMPANION_VERSION_JSON/COMPANION_LOADOUT_JSON pair meant the
// most-recently-installed companion silently overwrote every previously
// installed one's script into echoing ITS OWN content instead).
func (e *TestEnvironment) InstallFakeCompanion(bin, versionJSON, loadoutJSON string) error {
	dir, err := os.MkdirTemp(e.Root, "fake-companion-*")
	if err != nil {
		return fmt.Errorf("create fake companion dir: %w", err)
	}
	versionVar := companionEnvVar("COMPANION_VERSION_JSON", bin)
	loadoutVar := companionEnvVar("COMPANION_LOADOUT_JSON", bin)
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  version) printf '%%s' "$%s" ;;
  loadout) printf '%%s' "$%s" ;;
  *) exit 1 ;;
esac
`, versionVar, loadoutVar)
	path := filepath.Join(dir, bin)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write fake companion %q: %w", bin, err)
	}

	// The JSON payloads are handed to the script via env (rather than
	// inlined into the script text) so a loadout envelope's base64 bundle
	// payload — arbitrary bytes — never has to survive shell quoting.
	e.storeAndSetEnv(versionVar, versionJSON)
	e.storeAndSetEnv(loadoutVar, loadoutJSON)

	pathSep := string(os.PathListSeparator)
	current := os.Getenv("PATH")
	e.storeAndSetEnv("PATH", dir+pathSep+current)

	// EXEC CONSENT. Since the trust-on-first-use gate landed, a companion
	// ctxloom has never run is SKIPPED in a non-interactive session — which
	// every acceptance/integration invocation is — so installing the binary is
	// no longer enough to make it contribute anything. Record the consent the
	// same way the shipping CLI does, through the real writer against the
	// scenario's overridden HOME, so what these scenarios exercise is the
	// production consent path rather than a bypass. A fixture that granted
	// itself an exemption would prove the probe works while proving nothing
	// about the gate in front of it.
	//
	// Deliberately NOT applied to companions this environment did not install:
	// a real ltk on the developer's own PATH stays unconfirmed and skipped,
	// which is what keeps a scenario's result from depending on what the
	// machine happens to have.
	return e.GrantCompanionConsent(path)
}

// GrantCompanionConsent records, in the scenario's HOME, that ctxloom may
// execute the binary at path — the fixture form of `ctxloom trust companion
// allow <path>`. Exported so a scenario can grant consent for a binary it
// installed by some other means, and so a scenario that wants to observe the
// REFUSAL can deliberately not call it.
func (e *TestEnvironment) GrantCompanionConsent(path string) error {
	if _, err := config.SetCompanionConsent(path, true); err != nil {
		return fmt.Errorf("grant companion exec consent for %q: %w", path, err)
	}
	return nil
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
