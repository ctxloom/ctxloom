package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// back in place (see claudeCredentialMounts).
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
// resolves its own plan behind the engineContainerSpec.resolveAuth seam (claude:
// ANTHROPIC_* passthrough or ~/.claude mounts).
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
	// resolver sets the mode; claude's is read-WRITE (see claudeCredentialMounts).
	mounts []Mount
}

// claudeAuthEnvVars is the SCOPED set of Anthropic auth/config vars a claude run
// honors — the ONLY host env allowed to cross into the container for auth,
// distinct from the handshake-only containerHandshakeEnv (which deliberately
// DROPS ANTHROPIC_API_KEY). ANTHROPIC_API_KEY OR ANTHROPIC_AUTH_TOKEN presence is
// the trigger (a gateway host authenticates with AUTH_TOKEN+BASE_URL and carries
// no API key at all — see resolveClaudeContainerAuth); the rest cross only when
// also set. NEVER logged.
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

// noContainerAuth is the resolveAuth for a backend with NO registered
// container-auth spec at all: it always returns ok=false, so an
// unrecognized/unmapped engine's containerized run degrades honestly
// (a fatal ClassIsolation finding down the isolation chain, same as any other
// unresolvable auth) instead of silently inheriting the DEFAULT spec's
// resolveClaudeContainerAuth and mounting the user's Anthropic credentials
// into a foreign engine's container. Every backend that SHOULD authenticate
// must set its own explicit resolveAuth in engineContainerSpecFor.
func noContainerAuth(_ string, _ string) (containerAuth, bool) {
	return containerAuth{mode: authNone}, false
}

// resolveClaudeContainerAuth builds the auth plan for a containerized claude run
// whose fresh HOME is containerHome. It PREFERS env passthrough (an
// ANTHROPIC_API_KEY OR ANTHROPIC_AUTH_TOKEN in the host env — the latter covers a
// gateway host that authenticates via BASE_URL+AUTH_TOKEN and carries no API key)
// and otherwise falls back to BIND-MOUNTING the host's REAL subscription OAuth
// credential read-write into the container HOME (see claudeCredentialMounts —
// the container's token refresh writes back into the one real file so nothing
// desyncs the host's single-use rotating token). It returns ok=false only when
// NEITHER is available, so the caller errors and degrades down the chain to None
// rather than launching an unauthenticated engine that would hang or fail — a
// fatal finding (ClassIsolation) the choke owner aborts on unless --degraded,
// since the container was EXPLICITLY requested.
//
// The second parameter (the run's host-side scratch dir) is unused: the claude
// credential is now the real host file, not a staged copy, so nothing is written
// under scratch here. It is retained only to satisfy the shared resolveAuth seam
// signature (engineContainerSpec.resolveAuth).
func resolveClaudeContainerAuth(containerHome, _ string) (containerAuth, bool) {
	return resolveEnvOrMountAuth(
		[]string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		claudeAuthEnvVars,
		func() ([]Mount, bool) { return claudeCredentialMounts(containerHome) },
	)
}

// claudeContainerAuthHint is the claude spec's degrade diagnostic —
// platform-aware because the fallback credential path differs by OS. On
// darwin, a subscription login keeps its OAuth token in the macOS Keychain,
// NOT ~/.claude/.credentials.json — that file does not exist there, so naming
// it (the non-darwin hint below) is unfollowable advice. Extracting the
// Keychain token into a per-run scratch file is a real fix but needs a real
// Mac to verify (task sudsy-sip, a separate follow-up); until it lands, the
// only WORKING container auth on darwin is ANTHROPIC_API_KEY (or
// ANTHROPIC_AUTH_TOKEN), so the darwin hint names that instead of the file.
func claudeContainerAuthHint() string {
	if runtime.GOOS == "darwin" {
		return "no ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN to authenticate the in-container engine (a macOS Keychain-held subscription login cannot be mounted — set ANTHROPIC_API_KEY for a containerized run on Mac)"
	}
	return "no ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN and no ~/.claude credentials to authenticate the in-container engine"
}

// resolveMockContainerAuth builds the (trivial) auth plan for a containerized
// mock run: mock authenticates against NO vendor at all. internal/lm/backends'
// Mock is compiled directly into ctxloom and calls no external AI service (see
// backends/mock.go's package doc — it echoes fragments/context back and writes
// a record file); there is no API key, OAuth token, or credential file it could
// ever need. ok is therefore unconditionally true, and the plan is the
// unconditional zero value (authNone, no env, no mounts).
//
// This is deliberately NOT the same shape as noContainerAuth's
// ok=false: that default exists because an UNPROFILED engine's auth needs are
// UNKNOWN, and failing closed is the only safe answer until someone writes a
// real resolver for it (see that function's own doc). mock's needs are not
// unknown — they are KNOWN, and verified by reading its implementation, to be
// zero: a POSITIVE fact about this one backend, not an absence of policy.
// Every real engine resolver in this file returns ok=false on SOME path
// (missing env var, missing credential file); this is the only one that never
// does, and that is correct ONLY because mock has no vendor to authenticate
// against. Nothing about this function generalizes to a real engine — a
// resolver for a real engine that "resolved" no credentials MUST return
// ok=false (see every resolver above), never copy this shape.
func resolveMockContainerAuth(_ string, _ string) (containerAuth, bool) {
	return containerAuth{mode: authNone}, true
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

// claudeCredentialMounts builds the READ-WRITE credential mount that
// authenticates a subscription (OAuth) claude inside the container: the host's
// REAL ~/.claude/.credentials.json is bind-mounted DIRECTLY into containerHome,
// read-write, with NO intervening copy.
//
// This deliberately REVERSES the earlier copy-then-mount design for the
// container axis (RULED). claude refreshes its OAuth token in place,
// and that refresh token is SINGLE-USE and ROTATING: any COPY of the credential
// that refreshes mints a new token and INVALIDATES every other holder —
// including the host's own login. A read-write copy kept the refresh off the
// host file, but the container's refresh then rotated a token the host still
// believed current, silently logging the host out mid-session. Mounting the ONE
// real file means the container's refresh lands in the single source of truth:
// host and container share the same rotating token, nothing desyncs, and the
// host stays valid. The container therefore KEEPS refresh (no re-launch at
// expiry) — deliberately UNLIKE the host+worktree axes, which copy an
// ACCESS-TOKEN-ONLY credential (refresh stripped, see hostCredentialSeed and
// copyCredentialFile's projector) precisely because a copy THERE could rotate
// the host's single-use token. See docs/architecture/engines/isolation.md for
// the full three-axis model.
//
// The ctxloom-never-writes-real-home invariant HOLDS: ctxloom only DECLARES the
// bind mount; claude-the-binary writes the credential THROUGH it, exactly as it
// writes ~/.claude/.credentials.json on a non-containerized run. ctxloom itself
// never opens the real credential for writing.
//
// Only ~/.claude/.credentials.json ever crosses — never ~/.claude.json, which
// on a real host is claude's WHOLE top-level config including the user's OWN
// mcpServers registrations (and whatever secrets those carry); mounting it would
// hand every isolated agent read access to the user's personal integrations, a
// confidentiality leak, and .credentials.json alone is live-verified sufficient
// to authenticate (claude's descriptor says why on its Home declaration). Returns
// ok=false when the host OAuth token file is absent (nothing to mount).
func claudeCredentialMounts(containerHome string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	creds := filepath.Join(home, ".claude", ".credentials.json")
	if !fileExists(creds) {
		return nil, false
	}
	return []Mount{{
		Host:      creds,
		Container: filepath.Join(containerHome, ".claude", ".credentials.json"),
		ReadOnly:  false,
	}}, true
}

// fileExists reports whether path is an existing regular file (not a directory).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// copyCredentialFile copies src to dst at 0600 (owner-only). Reads the whole
// file into memory rather than streaming: credential files are tiny (a JSON
// token/account record, never a large blob), so the simplicity of
// read-then-write outweighs any streaming benefit, and it keeps the write
// atomic-enough for this use (no partial dst on a read failure).
//
// project, when non-nil, is the ENGINE's ambient-credential projection applied
// to the bytes AFTER reading src and BEFORE writing dst — claude strips its
// single-use refresh token here so a copied home cannot rotate the host's. src
// is only ever READ; the projection lands solely in dst. A projection error
// fails the copy loud (no dst is written) rather than falling back to the
// unprojected bytes, which for a security-motivated strip would be worse than
// not copying at all.
func copyCredentialFile(src, dst string, project func([]byte) ([]byte, error)) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if project != nil {
		data, err = project(data)
		if err != nil {
			return err
		}
	}
	// os.WriteFile follows a symlink at the destination (it is
	// OpenFile(dst, O_WRONLY|O_CREATE|O_TRUNC, perm) under the hood), so an
	// unvalidated destination — e.g. a repo-tracked `.claude/.credentials.json`
	// symlink pointing at the real `~/.claude/.credentials.json` — turns this seed into an
	// arbitrary-file overwrite of the user's own credential. Refuse a
	// pre-existing symlink destination outright rather than writing through
	// it; a fresh (non-symlink, non-existent) destination is unaffected.
	if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("credential seed destination %q is a symlink; refusing to write through it", dst)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return err
	}
	// os.WriteFile applies its perm argument only when it CREATES the file: a
	// destination that already existed keeps its own mode, so the 0600 above is
	// not a guarantee on its own. Restate it on the file that now holds live
	// credential bytes.
	return os.Chmod(dst, 0o600)
}
