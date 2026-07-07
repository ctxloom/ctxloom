package isolation

import (
	"os"
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
	// bind-mounted read-only into the container's fresh HOME.
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
// container: the scoped env vars to inject (env passthrough) and/or the read-only
// credential mounts to bind into the fresh HOME (subscription OAuth). Each engine
// resolves its own plan behind the containerProfile.resolveAuth seam (claude:
// ANTHROPIC_* passthrough or ~/.claude mounts; kiro: KIRO_API_KEY passthrough).
// Only the TRUSTED top-level run reaches it — low-trust fan-out auth
// (budget-capped per-agent keys, T1.5) is a separate, later concern.
type containerAuth struct {
	mode containerAuthMode
	// envPassthrough is the scoped set of auth env var NAMES (never "KEY=VAL")
	// forwarded name-only into the container via `docker/podman -e NAME`. The
	// runtime reads each VALUE from the launcher's OWN environment (the container
	// CLI ctxloom execs inherits os.Environ), so the secret value never lands in
	// the long-lived `run` argv — /proc/<pid>/cmdline is world-readable, whereas
	// the launcher's env (/proc/<pid>/environ) is owner-readable only. A value
	// must NEVER be stored here.
	envPassthrough []string
	mounts         []Mount // read-only credential mounts into the container HOME
}

// claudeAuthEnvVars is the SCOPED set of Anthropic auth/config vars a claude run
// honors — the ONLY host env allowed to cross into the container for auth,
// distinct from the handshake-only containerHandshakeEnv (which deliberately
// DROPS ANTHROPIC_API_KEY). ANTHROPIC_API_KEY presence is the trigger; the rest
// cross only when also set. NEVER logged.
var claudeAuthEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
}

// hostHomeDir is the seam over the host user's home directory (source of the
// mounted credentials). Overridable in tests.
var hostHomeDir = os.UserHomeDir

// resolveClaudeContainerAuth builds the auth plan for a containerized claude run
// whose fresh HOME is containerHome. It PREFERS env passthrough (an
// ANTHROPIC_API_KEY in the host env — the user's chosen default) and otherwise
// falls back to mounting the host's subscription OAuth credentials read-only into
// the container HOME. It returns ok=false only when NEITHER is available, so the
// caller errors and degrades down the chain to None rather than launching an
// unauthenticated engine that would hang or fail — a fatal finding
// (ClassIsolation) the choke owner aborts on unless --degraded, since the
// container was EXPLICITLY requested.
func resolveClaudeContainerAuth(containerHome string) (containerAuth, bool) {
	if names := presentEnvKeys(os.Getenv, claudeAuthEnvVars); os.Getenv("ANTHROPIC_API_KEY") != "" {
		return containerAuth{mode: authEnv, envPassthrough: names}, true
	}
	if mounts, ok := claudeCredentialMounts(containerHome); ok {
		return containerAuth{mode: authCredentialMount, mounts: mounts}, true
	}
	return containerAuth{mode: authNone}, false
}

// kiroAuthEnvVars is the SCOPED set of Kiro auth vars a kiro run honors — the
// only host env allowed to cross into the container for kiro auth. KIRO_API_KEY
// presence is the trigger (Kiro's headless mode skips the browser login when it
// is set). NEVER logged.
var kiroAuthEnvVars = []string{
	"KIRO_API_KEY",
}

// resolveKiroContainerAuth builds the auth plan for a containerized kiro run:
// KIRO_API_KEY env passthrough (headless mode) only. Subscription `kiro-cli
// login` credentials live under ~/.kiro ALONGSIDE session state the engine must
// write, so a wholesale read-only mount would break the engine and the exact
// credential file layout is unverified live — no credential-mount fallback until
// it is (the same doc-first posture as the kiro settings writer). ok=false →
// the caller degrades rather than launching an engine stuck at a browser login.
func resolveKiroContainerAuth(string) (containerAuth, bool) {
	if names := presentEnvKeys(os.Getenv, kiroAuthEnvVars); os.Getenv("KIRO_API_KEY") != "" {
		return containerAuth{mode: authEnv, envPassthrough: names}, true
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

// claudeCredentialMounts builds the read-only credential mounts that authenticate
// a subscription (OAuth) claude inside the container: the OAuth token file plus
// the ~/.claude.json account association, mapped into containerHome. Returns
// ok=false when the OAuth token file is absent (no subscription creds to mount).
// The mounts are READ-ONLY and scoped to the two credential files — the rest of
// the host's ~/.claude (transcripts, caches, project state) is NOT mounted, so the
// container HOME stays fresh except for these creds.
func claudeCredentialMounts(containerHome string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	creds := filepath.Join(home, ".claude", ".credentials.json")
	if !fileExists(creds) {
		return nil, false
	}
	mounts := []Mount{{
		Host:      creds,
		Container: filepath.Join(containerHome, ".claude", ".credentials.json"),
		ReadOnly:  true,
	}}
	// ~/.claude.json carries the OAuth account association claude reads at startup;
	// mount it read-only when present (it is not strictly required for the token
	// file alone, so its absence is not fatal).
	if dotClaude := filepath.Join(home, ".claude.json"); fileExists(dotClaude) {
		mounts = append(mounts, Mount{
			Host:      dotClaude,
			Container: filepath.Join(containerHome, ".claude.json"),
			ReadOnly:  true,
		})
	}
	return mounts, true
}

// fileExists reports whether path is an existing regular file (not a directory).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
