package isolation

import (
	"context"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"os"
	"path"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Controlled-home credential provisioning.
//
// The container path (auth.go) authenticates the container's OWN fresh HOME
// by BIND-MOUNTING host credential files into it. A CONTROLLED home — the
// per-session instance operations.ResolveInTreeAgentHome points the engine's
// home var at, on every cell — starts EMPTY. An engine that honours that var
// for CREDENTIALS too (not just config) then finds no creds there and starts
// logged out — silent unless something places material in the instance. That
// "something" is this file, reached through CopyAmbient.
//
// IT IS NOT A COPY, AND THAT IS THE WHOLE POINT. A copied credential had its
// single-use refresh token stripped out on the way across, so it worked until
// the access token expired and then that instance was stuck with no way back —
// not a weaker sharing mode, a different product with a fuse on it. The
// placement now goes through the material provisioner (Select, provisioner.go),
// which delivers the host credential by a mechanism that keeps host and
// instance on ONE rotating token: a mount shares it by IDENTITY, replication
// shares it by keeping the two in step. Either way a refresh the engine
// performs inside the instance is a refresh the host has too.
//
// WHICH mechanism is not decided here either. The engine DECLARES the
// deliveries it accepts, best first (hosting.Hosting.Provisioning), Select
// walks that declaration, and a platform that can honour none of it is a
// REFUSAL naming every candidate tried — never a quiet downgrade to something
// the engine did not agree to.
//
// WHAT to place is not decided here. Each engine declares its seed on its own
// home declaration (EngineFacts.Home's Credentials), read through the one
// facts accessor (enginefacts.go); CopyAmbient is handed a backend NAME and
// asks by it. Every engine the accessor knows has a declaration — provided
// OR declared absent with a reason — so a name it does not know is one
// nobody composed, never one somebody forgot.

// CredentialSeedEngineNames returns every engine the facts accessor knows,
// sorted: each has a credential-seed declaration, provided or absent.
func CredentialSeedEngineNames() []string {
	return factNames()
}

// seedFile is one host file a seed copies, resolved against the host HOME.
type seedFile struct {
	host     string // absolute host source path
	destName string // filename under the destination directory
	required bool
	project  func(host []byte) ([]byte, error) // the engine's projection, or nil
}

// resolveSeedFiles resolves seed's declared files against hostHome, in copy
// order.
func resolveSeedFiles(seed engine.CredentialSeed, hostHome string) []seedFile {
	out := make([]seedFile, 0, len(seed.Files))
	for _, f := range seed.Files {
		out = append(out, seedFile{
			host:     filepath.Join(hostHome, filepath.FromSlash(f.HostRelHome)),
			destName: f.DestName,
			required: f.Required,
			project:  f.Project,
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

// hostCredentialSeed places the host credential material seed declares for
// engine into configHome/<seed.Subdir>, gated on seed.EnvTrigger exactly as
// the container resolver gates its mount.
//
// It does not copy. It reads the engine's DECLARED provisioning policy and
// hands the work to whichever provisioner Select constructs for it, so the
// instance's credential and the host's are one rotating token rather than two
// diverging ones. A platform that can honour none of the declared deliveries
// is an ERROR naming every candidate tried — the run refuses rather than
// starting an engine on material that cannot renew.
//
// NEVER logs a secret value — only paths, and only the caller logs even those,
// via clidiag.Warn/strictness.Fail on the DECISION, not the content.
//
// The returned Result carries whatever the provisioning left RUNNING (a
// replicator's watchers). It is returned rather than closed here because the
// replication has to outlive this call: it is what propagates the engine's
// refreshes for as long as the engine runs.
func hostCredentialSeed(name string, seed engine.CredentialSeed, configHome string) (seedResult, Result, error) {
	if seed.EnvTrigger != "" && os.Getenv(seed.EnvTrigger) != "" {
		return seedSkippedEnv, Result{}, nil
	}
	files, ok := hostSeedSources(name, seed)
	if !ok {
		return seedNoSource, Result{}, nil
	}
	destDir := filepath.Join(configHome, seed.Subdir)
	if err := prepareSeedDir(name, destDir); err != nil {
		return seedNoSource, Result{}, err
	}
	if err := tightenSeedDestinations(name, destDir, files); err != nil {
		return seedNoSource, Result{}, err
	}
	return provisionSeedFiles(name, seed, files, configHome)
}

// provisionSeedFiles turns the resolved host files into declared Materials and
// has the engine's chosen provisioner place them.
//
// Every credential material asks for SharingShared: the host's refreshes
// must reach the instance for as long as the run lives. A file the engine
// projects (a withheld refresh token) is placed READ-ONLY, because its
// instance bytes are a lossy view of the host's and a write back through
// that view would strip the host's own copy — the projection decides the
// direction, not this function.
//
// Placing nothing reports seedNoSource, never seedOK: a placement that
// delivered zero bytes must not report success.
func provisionSeedFiles(name string, seed engine.CredentialSeed, files []seedFile, instanceHome string) (seedResult, Result, error) {
	materials := make([]Material, 0, len(files))
	for _, f := range files {
		if !fileExists(f.host) {
			continue // optional file absent — required ones are already known present
		}
		materials = append(materials, Material{
			Host: f.host,
			// Slash form, and the DECLARED destination name rather than one
			// re-derived from the host path: an name that renames material
			// on placement must be served at the name it actually reads.
			DestRel:  path.Join(seed.Subdir, f.destName),
			Sharing:  SharingShared,
			ReadOnly: f.project != nil,
			Project:  f.project,
		})
	}
	if len(materials) == 0 {
		return seedNoSource, Result{}, nil
	}
	prov, _, err := selectSeedProvisioner(name, seed)
	if err != nil {
		return seedNoSource, Result{}, err
	}
	res, err := prov.Provision(instanceHome, materials)
	if err != nil {
		return seedNoSource, Result{}, fmt.Errorf("provision %s credential material: %w", name, err)
	}
	return seedOK, res, nil
}

// selectSeedProvisioner constructs the provisioner for the deliveries the
// engine's seed declares it accepts. A seed with nothing it accepts cannot
// be authored (engine.HomeSpec.Validate refuses it), so the only failure
// here is Select's own: no declared candidate can serve the demand.
func selectSeedProvisioner(name string, seed engine.CredentialSeed) (Provisioner, Delivery, error) {
	return Select(context.Background(), seed.Accept, SharingShared, seedProvisionOptions()...)
}

// seedProvisionOptions are the facts this call site can state about the run.
//
// It states NONE of them, and each omission is a rejection with a reason
// rather than an oversight. This path prepares a HOST instance home, so there
// is no container home to emit mount descriptors against; and it does not
// launch the engine, so nothing here would PERFORM the imperative binds a
// namespace mount emits — declaring otherwise would stand up empty mount
// targets and start the engine logged out behind them.
//
// It is a var for ONE reason: the refusal is the contract that replaced the
// stripped copy, and a refusal that cannot be provoked is a claim nobody has
// checked. Production never replaces it.
var seedProvisionOptions = func() []ProvisionOption { return nil }

// tightenSeedDestinations restates owner-only on any destination that ALREADY
// EXISTS, before live credential bytes are placed into it.
//
// It is the file-level twin of prepareSeedDir's restated 0700, and it is not
// redundant: an in-place write sets the mode only on a file it CREATED, so a
// destination left behind by an earlier run at a looser mode would keep that
// mode while holding a live token. Every doc and every caller believes the
// owner-only guarantee holds unconditionally, so it is made to.
//
// It chmods ONLY regular files, decided by Lstat. A path-based chmod follows a
// symlink, so chmodding one would reach through to whatever it points at —
// the user's real credential, in the case the write itself refuses — and
// changing the mode of a file in the real host home is a write to the home
// this package never writes. A non-regular destination is left exactly as it
// is for the placement to refuse.
func tightenSeedDestinations(engine, destDir string, files []seedFile) error {
	for _, f := range files {
		dst := filepath.Join(destDir, f.destName)
		info, err := os.Lstat(dst)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := os.Chmod(dst, 0o600); err != nil {
			return fmt.Errorf("restrict %s credential destination %q: %w", engine, f.destName, err)
		}
	}
	return nil
}

// hostSeedSources resolves seed's host source files against the host HOME and
// reports whether there is anything seedable at all. ok=false is the
// "nothing to seed" degrade — an unresolvable/empty host HOME, or an absent
// REQUIRED file — never an error, because the caller must be free to proceed
// (the preparation seam turns it into the actionable error
// operations.ResolveInTreeAgentHome fails loud on).
func hostSeedSources(name string, seed engine.CredentialSeed) ([]seedFile, bool) {
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
			name, err)
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
//
// Flagged against tests/acceptance's seedDeadSession, which is MkdirAll
// followed by a WriteFile rather than a Chmod. What the two share is the shape
// of any function that makes two syscalls and wraps each error, not a concept;
// a helper covering both would have to take the second call as a parameter,
// and it would couple an acceptance fixture to this package to save one line.
// reprise:accept-drift
func prepareSeedDir(engine, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create %s credential seed dir: %w", engine, err)
	}
	if err := os.Chmod(destDir, 0o700); err != nil {
		return fmt.Errorf("restrict %s credential seed dir: %w", engine, err)
	}
	return nil
}
