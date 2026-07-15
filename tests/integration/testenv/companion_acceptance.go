//go:build integration || acceptance

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// reprise together, as J8's guardrails journey does) installs each fake
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
