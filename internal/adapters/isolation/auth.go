package isolation

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// containerAuthMode names HOW a container run authenticates the engine, for
// diagnostics only (the resolved secrets are never logged).
type containerAuthMode int

const (
	// authNone: no credentials could be resolved — the caller degrades to None.
	authNone containerAuthMode = iota
	// authEnv: the host's auth vars (the setup-token, an API key, a gateway
	// token) are passed through by name.
	authEnv
)

// String renders the auth mode for diagnostics (no secret values).
func (m containerAuthMode) String() string {
	if m == authEnv {
		return "env-passthrough"
	}
	return "none"
}

// containerAuth is the resolved plan for authenticating the engine INSIDE a
// container: the scoped env vars to forward. Each engine DECLARES its own
// plan (engine.ContainerAuth); resolveDeclaredAuth turns it into this. No
// credential file is ever mounted into a container (see enginetoken.go for
// why).
//
// The plan is DEPTH-BLIND: the session owner and every delegated agent
// resolve the SAME plan from the same host env. Nothing that reaches the
// resolver says where in the delegation tree a run sits.
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
	// value from OTHER USERS — but that is the weaker half of the story. It is NOT
	// protection from same-user processes (every agent and MCP server here runs as
	// this user) nor from children that inherit the env. The name-only rule is
	// therefore the actual guarantee, not a stylistic preference: a value stored
	// here would be written into argv, where owner-readability does not apply at
	// all. A value must NEVER be stored here.
	envPassthrough []string
}

// hostHomeDir is the seam over the host user's home directory (what an
// engine's instance-config writer reads ambient values from). Overridable in
// tests.
var hostHomeDir = os.UserHomeDir

// noContainerAuth is the resolveAuth for a backend with NO declared
// container-auth plan at all: it always returns ok=false, so an
// unrecognized/unmapped engine's containerized run degrades honestly
// (a fatal ClassIsolation finding down the isolation chain, same as any other
// unresolvable auth) instead of silently inheriting another engine's
// credentials into a foreign engine's container.
func noContainerAuth(map[string]string) (containerAuth, bool) {
	return containerAuth{mode: authNone}, false
}

// resolveDeclaredAuth builds the auth plan a containerized run of an engine
// gets from the engine's OWN declaration (engine.ContainerAuth): env
// passthrough when any declared trigger is set — in runAuth, the env the
// run's auth mode resolved to (engine.Auth.LaunchEnv), which carries the
// stored credential and is authoritative for every var it names, else in
// the host env — and ok=false otherwise, so the caller errors and degrades down the chain to None rather
// than launching an unauthenticated engine — a fatal finding
// (ClassIsolation) the choke owner aborts on unless --degraded, since the
// container was EXPLICITLY requested.
//
// A VENDORLESS declaration (a double that authenticates against nothing)
// resolves unconditionally to the empty plan: a POSITIVE fact the engine
// states about itself and engine.ContainerAuth.Validate holds exclusive of
// every other field.
//
// runAuth's VALUES reach the engine through the launch's own env, never the
// passthrough: a var runAuth names is withheld from the passthrough, so a
// shell export of a credential the mode blanked never enters the container.
func resolveDeclaredAuth(a engine.ContainerAuth, runAuth map[string]string) (containerAuth, bool) {
	if a.Vendorless != "" {
		return containerAuth{mode: authNone}, true
	}
	lookup := func(k string) string {
		if v, ok := runAuth[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
	for _, t := range a.EnvTriggers {
		if lookup(t) != "" {
			shellOnly := func(k string) string {
				if _, ok := runAuth[k]; ok {
					return ""
				}
				return os.Getenv(k)
			}
			return containerAuth{mode: authEnv, envPassthrough: presentEnvKeys(shellOnly, a.EnvPassthrough)}, true
		}
	}
	return containerAuth{mode: authNone}, false
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
