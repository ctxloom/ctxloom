package isolation

import (
	"fmt"
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
// KIRO_API_KEY env passthrough (headless mode) only, no credential-mount
// fallback. Verified live against an authenticated kiro-cli 2.12.1:
// subscription `kiro-cli login` (GitHub OAuth) credentials do NOT live under
// ~/.kiro at all — they live in $XDG_DATA_HOME/kiro-cli/data.sqlite3
// (default ~/.local/share/kiro-cli/data.sqlite3), a SQLite database (tables
// include auth_kv and state) that ALSO holds one of kiro's two session
// stores (conversations_v2 — see internal/kiro/session.go's package comment).
// ~/.kiro itself carries no credentials, only session/agent/settings state.
// No mount fallback is implemented here even now that the real path is
// known: KIRO_HOME (the env this package already relocates per-agent, see
// worktree.go) does NOT relocate $XDG_DATA_HOME, so per-agent isolation
// cannot scope this file without also wiring XDG_DATA_HOME through the
// isolation env — and bind-mounting a live SQLite database another kiro-cli
// process may have open (WAL mode) read-only into a container is an
// untested operational risk, not merely an untested path. ok=false → the
// caller degrades rather than launching an engine stuck at a browser login.
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

// --- Host+worktree credential seeding -------------------------------------
//
// The container path above authenticates a fresh, isolated HOME by BIND-
// MOUNTING host credential files into it (claudeCredentialMounts). A
// host+worktree run has no fresh HOME to mount into — it relocates the
// engine's config lookup via an env var (CLAUDE_CONFIG_DIR/CODEX_HOME/
// KIRO_HOME, see worktree.go's Env()) pointing at a per-agent scratch dir
// that starts EMPTY. An engine that honours the var for CREDENTIALS too
// (not just config) then finds no creds there and starts logged out — silent
// unless something seeds the dir. That "something" is this section: a COPY
// (never a symlink — the destination must stay WRITABLE so a token refresh
// lands in the per-agent copy, not back on the host's shared credential;
// see worktree.go's provisionConfigHome doc) of the same host credential
// material claudeCredentialMounts already knows how to find, gated on the
// SAME envTrigger precedence resolveClaudeContainerAuth uses.
//
// credentialSeedSpec is a per-engine descriptor answering the three
// questions worktree config-home provisioning needs: (1) does an env var
// already carry usable auth, bypassing seeding entirely (envTrigger); (2)
// what host file(s) hold the credential material, in copy order, and is each
// one REQUIRED (its absence means "nothing to seed") or best-effort/optional
// (e.g. an account-association file); (3) which config-home subdirectory the
// engine's isolation var points at (destSubdir — must match worktree.go's
// Env()). Only engines that (a) honour their isolation home-var for
// CREDENTIALS and (b) keep those credentials in copyable file(s) belong in
// credentialSeedSpecs below — see the registry doc for the engines
// deliberately left out and why.
type credentialSeedSpec struct {
	// engine names the backend for fail-loud messages (e.g. "claude") —
	// deliberately not the registered backend name (which is "claude-code"),
	// so messages read naturally.
	engine string
	// destSubdir is the config-home subdirectory the engine's isolation env
	// var is pointed at by worktree.go's Env() (e.g. "claude" for
	// CLAUDE_CONFIG_DIR). The seed lands here so Env()'s existing wiring picks
	// it up unchanged.
	destSubdir string
	// envTrigger is the env var whose presence means the engine already has
	// usable auth riding the process env (e.g. ANTHROPIC_API_KEY) — seeding
	// is skipped (not an error), mirroring resolveClaudeContainerAuth's
	// authEnv precedence (auth.go:82-83). "" if the engine has no such
	// bypass.
	envTrigger string
	// sourceFiles returns the host credential file(s) to copy, given the
	// host home directory, in copy order.
	sourceFiles func(hostHome string) []seedFile
}

// seedFile is one host file a credentialSeedSpec copies into the seeded
// config-home. required=true means its absence makes the WHOLE seed
// "nothing to seed" (the fail-loud case); required=false is copied
// best-effort when present (its absence alone is not fatal).
type seedFile struct {
	host     string // absolute host source path
	destName string // filename under the destination directory
	required bool
}

// credentialSeedSpecs is the registry provisionConfigHome (worktree.go)
// consults, keyed by the REGISTERED backend name (internal/lm/backends:
// "claude-code", "codex", "kiro" — see profile.go's containerProfileFor,
// which the same keys already drive). An engine with NO entry is not seeded
// at THIS layer:
//
//   - kiro: KIRO_HOME does not relocate credentials — subscription auth lives
//     in a GLOBAL sqlite under $XDG_DATA_HOME regardless of KIRO_HOME (see
//     resolveKiroContainerAuth's doc, verified live against kiro-cli 2.12.1).
//     A host+worktree kiro agent already shares the host's auth through that
//     global store; seeding would be both unnecessary and wrong (there is no
//     per-KIRO_HOME credential file to copy).
//   - codex: honours CODEX_HOME for credentials (auth.json), but ALREADY
//     seeds it through a SEPARATE, already-shipped mechanism —
//     internal/codex/backend.go's cellCodexHomeEnv + linkUserCodexAuth, which
//     redirect CODEX_HOME to <WorkDir>/.codex (not this package's configHome
//     — the two are different directories) and SYMLINK (not copy)
//     ~/.codex/auth.json in, because `codex exec` does not refresh tokens
//     (unlike claude, so the write-back hazard a symlink poses for claude
//     does not apply to codex the same way). That mechanism runs in
//     agent.LaunchBackend.ExecuteEnv AFTER this package's env and
//     unconditionally wins on the CODEX_HOME key for every isolated,
//     non-container run (ExecuteEnv: "later entries win on a key clash"; see
//     launch_backend.go), so registering codex here would seed a copy this
//     package's own Env() ships that the engine never reads — dead code.
//     Reconciling the two mechanisms (unify codex onto this copy-based
//     framework vs keep it on its own symlink path) is a real design
//     decision (deletes/relocates linkUserCodexAuth and decouples
//     CODEX_HOME from the cell-delivery dir that also carries codex's
//     config.toml/hooks/prompts) — NOT made in this registry; see the
//     grave-prize task notes.
var credentialSeedSpecs = map[string]credentialSeedSpec{
	"claude-code": {
		engine:     "claude",
		destSubdir: "claude",
		envTrigger: "ANTHROPIC_API_KEY",
		sourceFiles: func(hostHome string) []seedFile {
			return []seedFile{
				{
					host:     filepath.Join(hostHome, ".claude", ".credentials.json"),
					destName: ".credentials.json",
					required: true,
				},
				// ~/.claude.json carries the OAuth account association claude
				// reads at startup — optional. Live-verified (2026-07, claude
				// 2.1.210): claude auto-creates its own .claude.json inside
				// CLAUDE_CONFIG_DIR when the var is set and none exists there
				// yet (a fresh onboarding record), so its absence does NOT
				// block auth — .credentials.json alone was sufficient for a
				// `claude -p` run to authenticate and answer. Seeding it when
				// present just carries over the existing account
				// association/trust-dialog state instead of re-onboarding.
				{
					host:     filepath.Join(hostHome, ".claude.json"),
					destName: ".claude.json",
					required: false,
				},
			}
		},
	},
}

// seedResult is hostCredentialSeed's decision, returned instead of a bare
// bool so the caller (worktree.go) can tell "nothing to do" (seedSkippedEnv,
// seedNotApplicable) apart from "nothing WAS seedable" (seedNoSource) — only
// the latter is the fail-loud case.
type seedResult int

const (
	// seedSkippedEnv: the engine's envTrigger is set — auth rides the env,
	// nothing to seed. Not an error.
	seedSkippedEnv seedResult = iota
	// seedOK: at least the primary (required) credential file was copied.
	seedOK
	// seedNoSource: the engine DOES honour its isolation var for credentials,
	// no envTrigger is set, and the primary host credential file is absent —
	// nothing seedable. The caller fails loud (ClassIsolation).
	seedNoSource
)

// hostCredentialSeed seeds configHome/<spec.destSubdir> with spec's host
// credential material, gated on spec.envTrigger exactly as
// resolveClaudeContainerAuth gates the container mount. It copies (never
// symlinks — see the package doc above) each present file at 0600, owner-
// only: the destination holds live credential bytes and must not be group/
// world-readable even though the source file's own mode may differ. NEVER
// logs a secret value — only paths, and only the caller (worktree.go) logs
// even those, via clidiag.Warn/strictness.Fail on the DECISION, not the
// content.
func hostCredentialSeed(spec credentialSeedSpec, configHome string) (seedResult, error) {
	if spec.envTrigger != "" && os.Getenv(spec.envTrigger) != "" {
		return seedSkippedEnv, nil
	}
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return seedNoSource, nil
	}
	files := spec.sourceFiles(home)
	for _, f := range files {
		if f.required && !fileExists(f.host) {
			return seedNoSource, nil
		}
	}
	destDir := filepath.Join(configHome, spec.destSubdir)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return seedNoSource, fmt.Errorf("create %s credential seed dir: %w", spec.engine, err)
	}
	seededAny := false
	for _, f := range files {
		if !fileExists(f.host) {
			continue // optional file absent — already checked required above
		}
		if err := copyCredentialFile(f.host, filepath.Join(destDir, f.destName)); err != nil {
			return seedNoSource, fmt.Errorf("seed %s credential %q: %w", spec.engine, f.destName, err)
		}
		seededAny = true
	}
	if !seededAny {
		// Defensive: a spec with no required files and nothing present. No
		// current spec hits this (claude's primary file is required), but a
		// future all-optional spec should not report seedOK having copied
		// nothing.
		return seedNoSource, nil
	}
	return seedOK, nil
}

// copyCredentialFile copies src to dst at 0600 (owner-only). Reads the whole
// file into memory rather than streaming: credential files are tiny (a JSON
// token/account record, never a large blob), so the simplicity of
// read-then-write outweighs any streaming benefit, and it keeps the write
// atomic-enough for this use (no partial dst on a read failure).
func copyCredentialFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
