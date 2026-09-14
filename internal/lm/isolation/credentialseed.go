package isolation

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
// deliveries it accepts, best first (engine.Descriptor.Provisioning), Select
// walks that declaration, and a platform that can honour none of it is a
// REFUSAL naming every candidate tried — never a quiet downgrade to something
// the engine did not agree to.
//
// WHAT to place is not decided here. Each engine declares its seed on its own
// descriptor (engine.Descriptor.Home.Credentials), and internal/lm/backends
// pushes that declaration — provided OR declared absent — into this package
// for every engine it registers, alongside the provisioning policy. This
// package cannot import the registry (backends imports it), and CopyAmbient is
// handed a backend NAME, so a name-keyed table populated at registration is
// the only direction the wiring can run. The consequence is the invariant that
// matters: every registered engine has an entry here, so a name with no entry
// is one nobody registered, never one somebody forgot. An engine that keeps no
// seedable credential says so, with a reason, and that reason is readable back.

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
	credentialSeedMu.Lock()
	defer credentialSeedMu.Unlock()
	if !seed.Decided() {
		delete(credentialSeeds, engine)
		return
	}
	credentialSeeds[engine] = seed
}

// credentialSeedDeclared returns engine's declaration and whether the engine
// is registered at all. ok=false is "nobody registered this name"; a
// registered engine with no seed is ok=true with an absent declaration — the
// two are different answers.
func credentialSeedDeclared(engine string) (agent.Declared[agent.CredentialSeed], bool) {
	credentialSeedMu.RLock()
	defer credentialSeedMu.RUnlock()
	d, ok := credentialSeeds[engine]
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

// provisioningPolicies is each engine's DECLARED provisioning policy — the
// deliveries it will accept for its instance-home material, best first —
// pushed here by name for the identical reason the credential seed is (see
// this file's doc: isolation cannot import the registry, and CopyAmbient is
// handed a backend NAME with no descriptor in hand).
var (
	provisioningPolicyMu sync.RWMutex
	provisioningPolicies = map[string]agent.Declared[agent.ProvisioningPolicy]{}
)

// RegisterProvisioningPolicy installs engine's provisioning declaration.
// Called from internal/lm/backends' Register for EVERY descriptor, whether the
// policy is provided or declared absent; re-registering a name replaces it,
// and an undecided (zero) value deletes the entry, so a test can unwind its
// synthetic engine.
func RegisterProvisioningPolicy(engine string, policy agent.Declared[agent.ProvisioningPolicy]) {
	provisioningPolicyMu.Lock()
	defer provisioningPolicyMu.Unlock()
	if !policy.Decided() {
		delete(provisioningPolicies, engine)
		return
	}
	provisioningPolicies[engine] = policy
}

// provisioningPolicyDeclared returns engine's declaration and whether the
// engine is registered at this seam at all. ok=false is "nobody registered
// this name"; a registered engine that declared the slot ABSENT is ok=true
// with an absent declaration — and the two are different answers, because an
// engine with material to place and no accepted delivery is a contradiction
// worth naming, while an unregistered name is a wiring bug.
func provisioningPolicyDeclared(engine string) (agent.Declared[agent.ProvisioningPolicy], bool) {
	provisioningPolicyMu.RLock()
	defer provisioningPolicyMu.RUnlock()
	d, ok := provisioningPolicies[engine]
	return d, ok
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
func hostCredentialSeed(engine string, seed agent.CredentialSeed, configHome string) (seedResult, Result, error) {
	if seed.EnvTrigger != "" && os.Getenv(seed.EnvTrigger) != "" {
		return seedSkippedEnv, Result{}, nil
	}
	files, ok := hostSeedSources(engine, seed)
	if !ok {
		return seedNoSource, Result{}, nil
	}
	destDir := filepath.Join(configHome, seed.Subdir)
	if err := prepareSeedDir(engine, destDir); err != nil {
		return seedNoSource, Result{}, err
	}
	return provisionSeedFiles(engine, seed, files, configHome)
}

// provisionSeedFiles turns the resolved host files into declared Materials and
// has the engine's chosen provisioner place them.
//
// Every credential material asks for SharingShared, and it is not a choice
// this function makes: a credential that must RENEW has to reach the host, and
// a private one is the copy being deleted wearing a different name. Material
// deliberately isolated from the real thing is a different declaration, not a
// weaker version of this one.
//
// Placing nothing reports seedNoSource, never seedOK: a placement that
// delivered zero bytes must not report success.
func provisionSeedFiles(engine string, seed agent.CredentialSeed, files []seedFile, instanceHome string) (seedResult, Result, error) {
	materials := make([]Material, 0, len(files))
	for _, f := range files {
		if !fileExists(f.host) {
			continue // optional file absent — required ones are already known present
		}
		materials = append(materials, Material{
			Host: f.host,
			// Slash form, and the DECLARED destination name rather than one
			// re-derived from the host path: an engine that renames material
			// on placement must be served at the name it actually reads.
			DestRel: path.Join(seed.Subdir, f.destName),
			Sharing: SharingShared,
		})
	}
	if len(materials) == 0 {
		return seedNoSource, Result{}, nil
	}
	prov, _, err := selectSeedProvisioner(engine)
	if err != nil {
		return seedNoSource, Result{}, err
	}
	res, err := prov.Provision(instanceHome, materials)
	if err != nil {
		return seedNoSource, Result{}, fmt.Errorf("provision %s credential material: %w", engine, err)
	}
	return seedOK, res, nil
}

// selectSeedProvisioner reads engine's DECLARED policy and constructs the
// provisioner for it.
//
// The two ways this fails are told apart deliberately. An engine with material
// to place and no entry here at all is a WIRING bug — a name nobody registered
// — while an engine whose policy slot is declared ABSENT has stated it has
// nothing to provision, which contradicts its own credential seed and is worth
// saying in those words rather than failing later as "no candidates".
func selectSeedProvisioner(engine string) (Provisioner, Delivery, error) {
	declared, ok := provisioningPolicyDeclared(engine)
	if !ok {
		return nil, DeliveryUnset, fmt.Errorf(
			"%s credential provisioning: backend %q has no declared provisioning policy (internal error)", engine, engine)
	}
	policy, ok := declared.Get()
	if !ok {
		return nil, DeliveryUnset, fmt.Errorf(
			"%s credential provisioning: this engine declares credential material to place but declares no delivery it accepts (%s); one of the two declarations is wrong",
			engine, declared.AbsentReason())
	}
	return Select(context.Background(), policy, SharingShared, seedProvisionOptions()...)
}

// seedProvisionOptions are the facts this call site can state about the run.
//
// It states NONE of them, and each omission is a rejection with a reason
// rather than an oversight. This path prepares a HOST instance home, so there
// is no container home to emit mount descriptors against; and it does not
// launch the engine, so nothing here would PERFORM the imperative binds a
// namespace mount emits — declaring otherwise would stand up empty mount
// targets and start the engine logged out behind them.
func seedProvisionOptions() []ProvisionOption { return nil }

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
