//go:build integration || acceptance

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
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
func (e *TestEnvironment) InstallFakeCompanion(bin, versionJSON, loadoutJSON string) error {
	dir, err := os.MkdirTemp(e.Root, "fake-companion-*")
	if err != nil {
		return fmt.Errorf("create fake companion dir: %w", err)
	}
	const script = `#!/bin/sh
case "$1" in
  version) printf '%s' "$COMPANION_VERSION_JSON" ;;
  loadout) printf '%s' "$COMPANION_LOADOUT_JSON" ;;
  *) exit 1 ;;
esac
`
	path := filepath.Join(dir, bin)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write fake companion %q: %w", bin, err)
	}

	// The JSON payloads are handed to the script via env (rather than
	// inlined into the script text) so a loadout envelope's base64 bundle
	// payload — arbitrary bytes — never has to survive shell quoting.
	e.storeAndSetEnv("COMPANION_VERSION_JSON", versionJSON)
	e.storeAndSetEnv("COMPANION_LOADOUT_JSON", loadoutJSON)

	pathSep := string(os.PathListSeparator)
	current := os.Getenv("PATH")
	e.storeAndSetEnv("PATH", dir+pathSep+current)
	return nil
}
