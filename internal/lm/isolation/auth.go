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
// credential mounts to bind into the fresh HOME (subscription OAuth). It is
// deliberately claude-oriented for the P1 production image (claude first); a new
// engine adds its own trigger var + credential paths behind this same resolve
// seam. Only the TRUSTED top-level run reaches it — low-trust fan-out auth
// (budget-capped per-agent keys, T1.5) is a separate, later concern.
type containerAuth struct {
	mode   containerAuthMode
	env    []string // scoped "KEY=VAL" pairs to add to the container env
	mounts []Mount  // read-only credential mounts into the container HOME
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

// resolveContainerAuth builds the auth plan for a container run whose fresh HOME
// is containerHome. It PREFERS env passthrough (an ANTHROPIC_API_KEY in the host
// env — the user's chosen default) and otherwise falls back to mounting the
// host's subscription OAuth credentials read-only into the container HOME. It
// returns ok=false only when NEITHER is available, so the caller degrades to None
// rather than launching an unauthenticated engine that would hang or fail
// (CLAUDE.md fault tolerance).
func resolveContainerAuth(containerHome string) (containerAuth, bool) {
	if env := scopedEnv(os.Getenv, claudeAuthEnvVars); os.Getenv("ANTHROPIC_API_KEY") != "" {
		return containerAuth{mode: authEnv, env: env}, true
	}
	if mounts, ok := claudeCredentialMounts(containerHome); ok {
		return containerAuth{mode: authCredentialMount, mounts: mounts}, true
	}
	return containerAuth{mode: authNone}, false
}

// scopedEnv collects "KEY=VAL" for every key in keys that getenv reports as set
// (non-empty). The result is the scoped auth-env set — only known auth vars, so
// the host's full environment (secrets, paths) never blanket-crosses.
func scopedEnv(getenv func(string) string, keys []string) []string {
	var out []string
	for _, k := range keys {
		if v := getenv(k); v != "" {
			out = append(out, k+"="+v)
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
