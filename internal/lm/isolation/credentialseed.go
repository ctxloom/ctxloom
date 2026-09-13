package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Controlled-home credential seeding.
//
// The container path (auth.go) authenticates the container's OWN fresh HOME
// by BIND-MOUNTING host credential files into it. A CONTROLLED home — the
// per-session instance operations.ResolveInTreeAgentHome points the engine's
// home var at, on every cell — starts EMPTY. An engine that honours that var
// for CREDENTIALS too (not just config) then finds no creds there and starts
// logged out — silent unless something seeds the instance. That "something"
// is this file, reached through CopyAmbient: a COPY (never a symlink — the
// destination must stay WRITABLE so a token refresh lands in the instance's
// copy, not back on the host's shared credential) of the host credential
// material the engine DECLARES (agent.CredentialSeed), gated on the same
// env-trigger precedence the container resolver uses.
//
// WHAT to seed is not decided here. Each engine declares its seed on its own
// descriptor (engine.Descriptor.Home.Credentials), and internal/lm/backends
// pushes that declaration — provided OR declared absent — into this package
// for every engine it registers. This package cannot import the registry
// (backends imports it), and CopyAmbient is handed a backend NAME, so a
// name-keyed table populated at registration is the only direction the
// wiring can run. The consequence is the invariant that matters: every
// registered engine has an entry here, so a name with no entry is one nobody
// registered, never one somebody forgot. An engine that keeps no seedable
// credential says so, with a reason, and that reason is readable back.

var (
	credentialSeedMu sync.RWMutex
	credentialSeeds  = map[string]agent.Declared[agent.CredentialSeed]{}
)

// RegisterCredentialSeed installs engine's credential-seed declaration.
// Called from internal/lm/backends' Register for EVERY descriptor, whether
// the seed is provided or declared absent; re-registering a name replaces
// it, and an undecided (zero) value deletes the entry, so a test can unwind
// its synthetic engine.
func RegisterCredentialSeed(engine string, seed agent.Declared[agent.CredentialSeed]) {
	assertCanonicalEngineKey("credentialSeeds", engine)
	credentialSeedMu.Lock()
	defer credentialSeedMu.Unlock()
	if !seed.Decided() {
		delete(credentialSeeds, engine)
		return
	}
	credentialSeeds[engine] = seed
}

// credentialSeedDeclared returns engine's declaration and whether the engine
// is registered at all, resolving through the repo-wide alias table so an
// aliased spelling reaches the same entry. ok=false is "nobody registered
// this name"; a registered engine with no seed is ok=true with an absent
// declaration — the two are different answers.
func credentialSeedDeclared(engine string) (agent.Declared[agent.CredentialSeed], bool) {
	credentialSeedMu.RLock()
	defer credentialSeedMu.RUnlock()
	d, ok := credentialSeeds[agent.CanonicalEngineName(engine)]
	return d, ok
}

// credentialSeedFor returns engine's PROVIDED seed. ok=false covers both a
// declared absence and an unregistered name, which every caller here
// already treats the same way (nothing to seed); a caller that must tell
// them apart reads credentialSeedDeclared.
func credentialSeedFor(engine string) (agent.CredentialSeed, bool) {
	d, ok := credentialSeedDeclared(engine)
	if !ok {
		return agent.CredentialSeed{}, false
	}
	return d.Get()
}

// CredentialSeedEngineNames returns every engine with a declaration at this
// seam — provided or absent — sorted. It is the registry's own name list as
// far as this package can see it.
func CredentialSeedEngineNames() []string {
	credentialSeedMu.RLock()
	defer credentialSeedMu.RUnlock()
	names := make([]string, 0, len(credentialSeeds))
	for name := range credentialSeeds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// seedFile is one host file a seed copies, resolved against the host HOME.
type seedFile struct {
	host     string // absolute host source path
	destName string // filename under the destination directory
	required bool
}

// resolveSeedFiles resolves seed's declared files against hostHome, in copy
// order.
func resolveSeedFiles(seed agent.CredentialSeed, hostHome string) []seedFile {
	out := make([]seedFile, 0, len(seed.Files))
	for _, f := range seed.Files {
		out = append(out, seedFile{
			host:     filepath.Join(hostHome, filepath.FromSlash(f.HostRelHome)),
			destName: f.DestName,
			required: f.Required,
		})
	}
	return out
}

// seedResult is hostCredentialSeed's decision, returned instead of a bare
// bool so the caller (CopyAmbient) can tell "nothing to do" (seedSkippedEnv)
// apart from "nothing WAS seedable" (seedNoSource) — only the latter is the
// fail-loud case.
type seedResult int

const (
	// seedSkippedEnv: the engine's EnvTrigger is set — auth rides the env,
	// nothing to seed. Not an error.
	seedSkippedEnv seedResult = iota
	// seedOK: at least the primary (required) credential file was copied.
	seedOK
	// seedNoSource: the engine DOES honour its isolation var for credentials,
	// no EnvTrigger is set, and the primary host credential file is absent —
	// nothing seedable. The caller fails loud (ClassIsolation).
	seedNoSource
)

// hostCredentialSeed seeds configHome/<seed.Subdir> with the host credential
// material seed declares for engine, gated on seed.EnvTrigger exactly as the
// container resolver gates its mount. It copies (never symlinks — see the
// file doc above) each present file at 0600, owner-only: the destination
// holds live credential bytes and must not be group/world-readable even
// though the source file's own mode may differ. NEVER logs a secret value —
// only paths, and only the caller logs even those, via
// clidiag.Warn/strictness.Fail on the DECISION, not the content.
func hostCredentialSeed(engine string, seed agent.CredentialSeed, configHome string, projector agent.CredentialProjector) (seedResult, error) {
	if seed.EnvTrigger != "" && os.Getenv(seed.EnvTrigger) != "" {
		return seedSkippedEnv, nil
	}
	files, ok := hostSeedSources(engine, seed)
	if !ok {
		return seedNoSource, nil
	}
	destDir := filepath.Join(configHome, seed.Subdir)
	if err := prepareSeedDir(engine, destDir); err != nil {
		return seedNoSource, err
	}
	return copySeedFiles(engine, files, destDir, projector)
}

// hostSeedSources resolves seed's host source files against the host HOME and
// reports whether there is anything seedable at all. ok=false is the
// "nothing to seed" degrade — an unresolvable/empty host HOME, or an absent
// REQUIRED file — never an error, because the caller must be free to proceed
// (worktree.go turns it into its own fail-loud decision).
func hostSeedSources(engine string, seed agent.CredentialSeed) ([]seedFile, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		// Still a degrade, never an abort (the caller must not be blocked by a
		// HOME lookup). But an unresolvable host HOME is an ENVIRONMENT FAULT,
		// not the ordinary "this host has no credential file" the caller's own
		// message describes — leaving it silent surfaced a real fault as advice
		// to run the login command, which cannot help. Same handling
		// provisionCuratedHome gives the identical failure.
		clidiag.Warn("ctxloom",
			"%s credential seed: could not resolve the host HOME to copy credentials from (%v); this run is treated as having no host credentials to seed",
			engine, err)
		return nil, false
	}
	files := resolveSeedFiles(seed, home)
	for _, f := range files {
		if f.required && !fileExists(f.host) {
			return nil, false
		}
	}
	return files, true
}

// prepareSeedDir creates the per-engine destination and makes it owner-only.
// MkdirAll's perm argument, like os.WriteFile's, applies only on CREATION — a
// pre-existing dir keeps its own mode — so the 0700 is restated rather than
// assumed on a directory about to hold live credential files.
func prepareSeedDir(engine, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create %s credential seed dir: %w", engine, err)
	}
	if err := os.Chmod(destDir, 0o700); err != nil {
		return fmt.Errorf("restrict %s credential seed dir: %w", engine, err)
	}
	return nil
}

// copySeedFiles copies each PRESENT source file into destDir (required ones are
// already known present — see hostSeedSources). A copy failure is an error, not
// a degrade: the material exists and we failed to place it. Copying nothing
// reports seedNoSource, never seedOK: a seed that delivered zero bytes must
// not report success.
func copySeedFiles(engine string, files []seedFile, destDir string, projector agent.CredentialProjector) (seedResult, error) {
	seededAny := false
	for _, f := range files {
		if !fileExists(f.host) {
			continue // optional file absent — already checked required above
		}
		// The engine's projector (claude's refresh-token strip) transforms the
		// bytes as they cross; an engine with no projector copies verbatim. The
		// projection is keyed by the engine's own destName, so a projector that
		// only cares about one of several ambient files passes the rest through.
		var project func([]byte) ([]byte, error)
		if projector != nil {
			destName := f.destName
			project = func(b []byte) ([]byte, error) { return projector.ProjectAmbientCredential(destName, b) }
		}
		if err := copyCredentialFile(f.host, filepath.Join(destDir, f.destName), project); err != nil {
			return seedNoSource, fmt.Errorf("seed %s credential %q: %w", engine, f.destName, err)
		}
		seededAny = true
	}
	if !seededAny {
		return seedNoSource, nil
	}
	return seedOK, nil
}
