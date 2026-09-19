//go:build integration || acceptance

package testenv

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
)

// InstallFakeCompanion writes an executable shell script named bin (e.g.
// "reprise") into a fresh directory prepended to this PROCESS's PATH — so
// every subprocess this environment spawns (env.Run/RunPTY/Command, all of
// which build their env from os.Environ() at call time) finds it exactly as
// it would find a real companion binary on the developer's machine.
// versionJSON/loadoutJSON are the literal stdout this fake emits for
// `<bin> version --format json` / `<bin> loadout --format json` — the two
// companion-loadout-protocol subcommands config.DiscoverCompanions/
// ProbeCompanionLoadouts actually exec (see internal/core/config/companions.go).
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

	// EXEC ADMISSION. A companion is executed only when its bytes carry a
	// signature from a key the scenario's trust root authorizes, so installing
	// the binary is not enough to make it contribute anything — it has to be
	// vouched for. Sign it the way a publisher does, with a key trusted only in
	// THIS scenario, so what these scenarios exercise is the production
	// admission path rather than a bypass. A fixture that granted itself an
	// exemption would prove the probe works while proving nothing about the
	// gate in front of it.
	//
	// Deliberately NOT applied to companions this environment did not install:
	// a real ltk on the developer's own PATH is signed by a key this scenario
	// does not trust, so it stays refused — which is what keeps a scenario's
	// result from depending on what the machine happens to have.
	return e.signCompanion(path)
}

// SignCompanion signs a binary a scenario installed by some other means, so
// ctxloom will execute it. Exported for the same reason GrantCompanionConsent
// was: a scenario that wants to observe the REFUSAL simply does not call it.
func (e *TestEnvironment) SignCompanion(path string) error { return e.signCompanion(path) }

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

// --- companion signing, the fixture form of a publisher vouching for bytes ---
//
// Admission is answered by a SIGNATURE from a key the scenario's trust root
// authorizes for the companion namespace. So a fixture that wants its fake
// companion to run has to do what a real publisher does: sign the bytes, and be
// trusted for that namespace. Recording consent is no longer a thing that can
// be done — the approve path is gone, because a signature is the approval.
//
// The key is generated PER ENVIRONMENT and trusted only in that scenario's own
// allowed_signers, so nothing here depends on the developer's keys and two
// scenarios cannot vouch for each other's binaries.

// signCompanion signs the binary at path with this environment's fixture key,
// leaving the detached `<path>.sig` admission reads, and ensures the scenario's
// trust root authorizes that key for the companion namespace.
func (e *TestEnvironment) signCompanion(path string) error {
	signer, err := e.companionSigner()
	if err != nil {
		return err
	}
	payload, err := os.ReadFile(path) //nolint:gosec // fixture path built by this package
	if err != nil {
		return fmt.Errorf("read %q to sign it: %w", path, err)
	}
	sig, err := signing.Sign(payload, signer, signing.NamespaceCompanion)
	if err != nil {
		return fmt.Errorf("sign companion %q: %w", path, err)
	}
	if err := os.WriteFile(path+".sig", sig, 0o600); err != nil {
		return fmt.Errorf("write signature for %q: %w", path, err)
	}
	return nil
}

// companionSigner mints this environment's fixture signing key once and trusts
// it in the scenario's own allowed_signers.
func (e *TestEnvironment) companionSigner() (ssh.Signer, error) {
	if e.companionKey != nil {
		return e.companionKey, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate the fixture companion key: %w", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		return nil, fmt.Errorf("wrap the fixture companion key: %w", err)
	}
	line := fmt.Sprintf("%s namespaces=%q %s %s\n",
		fixtureCompanionPrincipal, signing.NamespaceCompanion,
		signer.PublicKey().Type(),
		base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()))

	// The HOME trust root, not a project one. A scenario can hold SEVERAL
	// checkouts — the onboarding journey gives Alice and Bob one each — and a
	// companion installed once on the machine has to be admissible in all of
	// them. Writing into one project dir vouched for the binary in exactly one
	// checkout, so the other saw a signature by an unknown key and refused it.
	//
	// This is still scenario-scoped: HOME is the fake home this environment
	// created and cleans up, so nothing leaks to the developer's own.
	allowed := filepath.Join(e.HomeDir, ".ctxloom", "allowed_signers")
	if err := os.MkdirAll(filepath.Dir(allowed), 0o755); err != nil {
		return nil, fmt.Errorf("create the scenario trust root: %w", err)
	}
	f, err := os.OpenFile(allowed, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the scenario trust root: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		return nil, fmt.Errorf("trust the fixture companion key: %w", err)
	}
	e.companionKey = signer
	return signer, nil
}

// fixtureCompanionPrincipal names the fixture publisher in a scenario's trust
// root. It is deliberately not a real address: a principal that looked real
// would be one a reader could mistake for a key this project actually trusts.
const fixtureCompanionPrincipal = "fixture-companion@testenv.invalid"
