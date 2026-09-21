package isolation

import (
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"os"
	"path"
	"path/filepath"
)

// containerAuthMode names HOW a container run authenticates the engine, for
// diagnostics only (the resolved secrets/paths are never logged).
type containerAuthMode int

const (
	// authNone: no credentials could be resolved — the caller degrades to None.
	authNone containerAuthMode = iota
	// authEnv: the host's ANTHROPIC_* vars are passed through (the user's chosen
	// default, used when ANTHROPIC_API_KEY is present).
	authEnv
	// authCredentialMount: the host's subscription OAuth credentials are
	// bind-mounted into the container's fresh HOME — read-only for engines whose
	// non-interactive mode never refreshes, read-WRITE and
	// pointed at the REAL host file for claude, whose token refresh must write
	// back in place (see credentialFileMounts).
	authCredentialMount
)

// String renders the auth mode for diagnostics (no secret values).
func (m containerAuthMode) String() string {
	switch m {
	case authEnv:
		return "env-passthrough"
	case authCredentialMount:
		return "credential-mount"
	default:
		return "none"
	}
}

// containerAuth is the resolved plan for authenticating the engine INSIDE a
// container: the scoped env vars to inject (env passthrough) and/or the
// credential mounts to bind into the fresh HOME (subscription OAuth). Each engine
// DECLARES its own plan (engine.ContainerAuth); resolveDeclaredAuth turns it
// into this.
//
// The plan is DEPTH-BLIND, and that is a ruling (full credential parity at
// every delegation depth, no trust gate), not an omission: the session owner
// and every delegated agent, however deep in the delegation tree, resolve the
// SAME plan from the same host env and host home, and so authenticate with the
// same credential under the same mount mode. Nothing that reaches the resolver
// says where in the tree a run sits — Prepare carries axes, backend, agent id
// and session state, none of which encode depth or trust, and the resolver
// reads only the host env and the container home.
// TestContainerMount_CredentialMountIsReadWriteAtEveryDelegationDepth pins the
// parity against the rendered MountPlan.
type containerAuth struct {
	mode containerAuthMode
	// envPassthrough is the scoped set of auth env var NAMES (never "KEY=VAL")
	// forwarded name-only into the container via `docker/podman -e NAME`. The
	// runtime reads each VALUE from the launcher's OWN environment (the container
	// CLI ctxloom execs inherits os.Environ), so the secret value never lands in
	// the long-lived `run` argv, which really is world-readable
	// (/proc/<pid>/cmdline).
	//
	// The launcher's env (/proc/<pid>/environ) is mode 0400, so this does keep the
	// value from OTHER USERS — but that is the weaker half of the story and this
	// comment used to stop there. It is NOT protection from same-user processes
	// (every agent and MCP server here runs as this user) nor from children that
	// inherit the env. The name-only rule below is therefore the actual guarantee,
	// not a stylistic preference: a value stored here would be written into argv,
	// where owner-readability does not apply at all. A value must NEVER be stored
	// here.
	envPassthrough []string
	// mounts are the credential mounts into the container HOME. Each engine's
	// resolver sets the mode; claude's is read-WRITE (see credentialFileMounts).
	mounts []Mount
}

// hostHomeDir is the seam over the host user's home directory (source of the
// mounted credentials). Overridable in tests.
var hostHomeDir = os.UserHomeDir

// resolveEnvOrMountAuth is the common shape every per-engine resolver below
// reduces to: prefer a scoped env passthrough when ANY of triggers is set in
// the host env, else fall back to a mount lookup (nil = no mount fallback),
// else degrade (ok=false). One shared shape rather than a near-identical
// resolver repeated per engine.
func resolveEnvOrMountAuth(triggers []string, envVars []string, mountFn func() ([]Mount, bool)) (containerAuth, bool) {
	triggered := false
	for _, t := range triggers {
		if os.Getenv(t) != "" {
			triggered = true
			break
		}
	}
	if triggered {
		return containerAuth{mode: authEnv, envPassthrough: presentEnvKeys(os.Getenv, envVars)}, true
	}
	if mountFn != nil {
		if mounts, ok := mountFn(); ok {
			return containerAuth{mode: authCredentialMount, mounts: mounts}, true
		}
	}
	return containerAuth{mode: authNone}, false
}

// noContainerAuth is the resolveAuth for a backend with NO declared
// container-auth plan at all: it always returns ok=false, so an
// unrecognized/unmapped engine's containerized run degrades honestly
// (a fatal ClassIsolation finding down the isolation chain, same as any other
// unresolvable auth) instead of silently inheriting another engine's
// credentials into a foreign engine's container. Every backend that SHOULD
// authenticate declares its own plan (hosting.Hosting.Container.Auth).
func noContainerAuth(_ string, _ string) (containerAuth, bool) {
	return containerAuth{mode: authNone}, false
}

// resolveDeclaredAuth builds the auth plan a containerized run of an engine
// gets from the engine's OWN declaration (engine.ContainerAuth), whose fresh
// HOME is containerHome. It PREFERS env passthrough (any declared trigger set
// in the host env) and otherwise falls back to BIND-MOUNTING the declared
// host credential files into the container HOME. It returns ok=false only
// when NEITHER is available, so the caller errors and degrades down the chain
// to None rather than launching an unauthenticated engine that would hang or
// fail — a fatal finding (ClassIsolation) the choke owner aborts on unless
// --degraded, since the container was EXPLICITLY requested.
//
// A VENDORLESS declaration (a double that authenticates against nothing)
// resolves unconditionally to the empty plan: a POSITIVE fact the engine
// states about itself and engine.ContainerAuth.Validate holds exclusive of
// every other field — never the shape a real engine's plan may take.
func resolveDeclaredAuth(a engine.ContainerAuth, containerHome string) (containerAuth, bool) {
	if a.Vendorless != "" {
		return containerAuth{mode: authNone}, true
	}
	var mountFn func() ([]Mount, bool)
	if len(a.CredentialFiles) > 0 {
		mountFn = func() ([]Mount, bool) { return credentialFileMounts(a.CredentialFiles, containerHome) }
	}
	return resolveEnvOrMountAuth(a.EnvTriggers, a.EnvPassthrough, mountFn)
}

// presentEnvKeys returns the subset of keys that getenv reports as set
// (non-empty), in order. It is the shared filter behind two container-env
// forwards that differ only in the -e form they emit: the auth passthrough
// (containerAuth.envPassthrough) forwards each name-only via `docker/podman -e
// NAME`, so the secret VALUE stays out of the world-readable run argv
// (/proc/<pid>/cmdline) and lives only in the launcher's env; hostTerminalEnv
// forwards TERM/COLORTERM as `-e KEY=VAL`. Callers pass a SCOPED key allowlist,
// so the host's full environment (other secrets, paths) never blanket-crosses.
func presentEnvKeys(getenv func(string) string, keys []string) []string {
	var out []string
	for _, k := range keys {
		if getenv(k) != "" {
			out = append(out, k)
		}
	}
	return out
}

// credentialFileMounts builds the bind mounts that put an engine's declared
// host credential FILES into the container's own, UNRELOCATED $HOME: each is
// the REAL host file, mounted DIRECTLY with NO intervening copy, at the mode
// the engine declared.
//
// THIS IS THE UNSAFE SELECTION'S MOUNT, and nothing else's (ruled
// 2026-09-21). A container run whose binding selected `engine_home: host`
// keeps the container's fresh $HOME and authenticates from the real file
// through it — the run IS the human's real home, by selection. Every other
// container run relocates its home to its session home, and MountEngineHome
// drops these mounts the moment it does: the session home is then the only
// credential source (the orchestrator's whole copy, or an agent's read-only
// projection), and the real host file is never a mount source.
//
// The ctxloom-never-writes-real-home invariant HOLDS: ctxloom only DECLARES
// the bind mount; the engine binary writes through it exactly as it writes
// its own home on a non-containerized run.
//
// Only the files the engine LISTS ever cross — an allow-list, never a
// deny-list; claude's descriptor says why it lists .credentials.json and not
// .claude.json. ok=false when any listed host file is absent (a bind mount of
// a missing file would create a directory in its place). ContainerRelHome is
// a CONTAINER path, joined with forward slashes whatever the host separator,
// and it is the file's place in the UNRELOCATED $HOME layout — joined WHOLE,
// never trimmed to a leaf.
func credentialFileMounts(files []engine.CredentialFile, containerHome string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	wanted := make([]Mount, 0, len(files))
	for _, f := range files {
		wanted = append(wanted, Mount{
			Host:      filepath.Join(home, filepath.FromSlash(f.HostRelHome)),
			Container: path.Join(containerHome, f.ContainerRelHome),
			ReadOnly:  f.ReadOnly,
		})
	}
	mounts, missing := bindFiles(wanted)
	return mounts, len(missing) == 0
}

// bindFiles keeps the mounts whose host file exists and reports, by host
// path, the ones whose file does not — a bind mount of a missing file would
// create a directory in its place, so the caller decides whether an absence
// refuses the whole set or is merely said out loud.
func bindFiles(wanted []Mount) (mounts []Mount, missing []string) {
	for _, m := range wanted {
		if fileExists(m.Host) {
			mounts = append(mounts, m)
		} else {
			missing = append(missing, m.Host)
		}
	}
	return mounts, missing
}

// fileExists reports whether path is an existing regular file (not a directory).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
