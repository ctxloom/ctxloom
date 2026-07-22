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
// the host env, else fall back to a mount lookup (nil = no mount fallback,
// kiro's case), else degrade (ok=false). Factored out after reprise flagged
// the codex/opencode resolvers as exact-normalized duplicates of each other;
// claude and kiro are folded in too rather than leaving three near-identical
// shapes alongside one shared one.
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

// resolveClaudeContainerAuth builds the auth plan for a containerized claude run
// whose fresh HOME is containerHome. It PREFERS env passthrough (an
// ANTHROPIC_API_KEY OR ANTHROPIC_AUTH_TOKEN in the host env — the latter covers a
// gateway host that authenticates via BASE_URL+AUTH_TOKEN and carries no API key)
// and otherwise falls back to COPYING the host's subscription OAuth credentials
// into scratchDir and mounting the copies read-write into the container HOME (see
// claudeCredentialCopyMounts — a plain read-only mount of the host originals would
// collide with claude's token-refresh write-back). It returns ok=false only when
// NEITHER is available, so the caller errors and degrades down the chain to None
// rather than launching an unauthenticated engine that would hang or fail — a
// fatal finding (ClassIsolation) the choke owner aborts on unless --degraded,
// since the container was EXPLICITLY requested.
func resolveClaudeContainerAuth(containerHome, scratchDir string) (containerAuth, bool) {
	return resolveEnvOrMountAuth(
		[]string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		claudeAuthEnvVars,
		func() ([]Mount, bool) { return claudeCredentialCopyMounts(containerHome, scratchDir) },
	)
}

// claudeContainerAuthHint is the claude profile's degrade diagnostic —
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

// kiroAuthEnvVars is the SCOPED set of Kiro auth vars a kiro run honors — the
// only host env allowed to cross into the container for kiro auth. KIRO_API_KEY
// presence is the TRIGGER (Kiro's headless mode skips the browser login when it
// is set); the AWS_* vars ride ALONG when present (a Bedrock-backed kiro
// configuration reads them like any AWS SDK client) but are deliberately NOT a
// standalone trigger of their own yet — whether AWS credentials alone (no
// KIRO_API_KEY) authenticate a containerized kiro needs a live kiro verification
// (task numb-panda) this package cannot run hermetically; treating them as a
// trigger without that confirmation risks launching a container that starts but
// never actually authenticates, a NEW silent-no-op. AWS_PROFILE alone is
// forwarded but is likely insufficient on its own: profile-based auth resolves
// against ~/.aws/{config,credentials}, which this package does NOT mount —
// explicit key vars (or KIRO_API_KEY) are the supported path until that's
// revisited. NEVER logged.
var kiroAuthEnvVars = []string{
	"KIRO_API_KEY",
	"AWS_REGION",
	"AWS_DEFAULT_REGION",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AWS_PROFILE",
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
// KIRO_API_KEY stays the sole trigger (see kiroAuthEnvVars' doc for why AWS_*
// alone does not yet trigger); when it is set, any present AWS_* vars ride
// along in the same passthrough for a Bedrock-backed configuration.
func resolveKiroContainerAuth(string, string) (containerAuth, bool) {
	return resolveEnvOrMountAuth([]string{"KIRO_API_KEY"}, kiroAuthEnvVars, nil)
}

// codexAuthEnvVars is the SCOPED set of codex auth vars a codex run honors —
// the only host env allowed to cross into the container for codex auth.
// OPENAI_API_KEY is the trigger, live-verified against `codex doctor`
// (credentialSeedSpecs["codex"]'s envTrigger doc: "codex doctor reports 'auth
// is provided by environment' with a fresh CODEX_HOME + OPENAI_API_KEY set,
// no `codex login` needed"). CODEX_API_KEY is NOT a confirmed codex trigger —
// it appears nowhere in internal/codex's own env handling — so it is
// deliberately excluded here rather than guessed in. NEVER logged.
var codexAuthEnvVars = []string{
	"OPENAI_API_KEY",
}

// resolveCodexContainerAuth builds the auth plan for a containerized codex run
// whose fresh HOME is containerHome (bony-spoof). It PREFERS env passthrough
// (OPENAI_API_KEY) and otherwise falls back to mounting the host's
// ~/.codex/auth.json READ-ONLY into the container HOME (codexCredentialMounts).
// Unlike claude's subscription refresh, the read-only mount is SAFE here:
// `codex exec`/non-interactive mode never refreshes the ChatGPT auth token in
// place (balmy-comic), so there is no write-back to collide with — codex does
// not need claude's copy-then-mount-rw treatment. Returns ok=false when
// neither is available, so the caller degrades rather than launching an
// unauthenticated engine.
func resolveCodexContainerAuth(containerHome, _ string) (containerAuth, bool) {
	return resolveEnvOrMountAuth(
		[]string{"OPENAI_API_KEY"},
		codexAuthEnvVars,
		func() ([]Mount, bool) { return codexCredentialMounts(containerHome) },
	)
}

// codexCredentialMounts builds the read-only credential mount that
// authenticates a subscription codex inside the container:
// ~/.codex/auth.json mapped into containerHome/.codex/auth.json. It reuses
// credentialSeedSpecs["codex"]'s sourceFiles descriptor — the SAME host-file
// location the host+worktree credential seed path (grave-prize,
// hostCredentialSeed below) already knows — instead of hard-coding the path a
// second time, per the plan's "one source-discovery, not two"
// (container-image-auth-delivery.plan.md §2.2). Returns ok=false when the
// file is absent or the codex spec is missing/has no sourceFiles (defensive;
// should never happen — codex's spec always sets both).
func codexCredentialMounts(containerHome string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	spec, ok := credentialSeedSpecs["codex"]
	if !ok || spec.sourceFiles == nil {
		return nil, false
	}
	var mounts []Mount
	for _, f := range spec.sourceFiles(home) {
		if !f.required {
			continue // codex's spec has one required file (auth.json) today; a future optional entry is account-association residue, not the credential itself
		}
		if !fileExists(f.host) {
			return nil, false
		}
		mounts = append(mounts, Mount{
			Host:      f.host,
			Container: filepath.Join(containerHome, ".codex", f.destName),
			ReadOnly:  true,
		})
	}
	if len(mounts) == 0 {
		return nil, false
	}
	return mounts, true
}

// opencodeAuthEnvVars is the SCOPED set of opencode auth vars a containerized
// opencode run honors. OPENROUTER_API_KEY is the trigger: OpenRouter is
// ctxloom's documented default opencode provider. opencode's own provider
// config can in principle honor other providers' native env vars too, but
// only OpenRouter is wired here until a broader multi-provider container auth
// story is designed. NEVER logged.
var opencodeAuthEnvVars = []string{
	"OPENROUTER_API_KEY",
}

// resolveOpencodeContainerAuth builds the auth plan for a containerized
// opencode run whose fresh HOME is containerHome. It PREFERS env passthrough
// (OPENROUTER_API_KEY) and otherwise falls back to mounting the host's seeded
// ~/.local/share/opencode/auth.json (the file `opencode auth login` writes)
// READ-ONLY into the container HOME. opencode has no credentialSeedSpecs entry
// (host+worktree isolation does not relocate its creds today — a separate,
// undecided workstream), so opencodeCredentialMounts mirrors
// claudeCredentialCopyMounts' host-file discovery shape directly rather than
// reusing that registry. The mount stays read-only: whether a non-interactive
// opencode run refreshes auth.json in place is unverified (no live-verified
// evidence either way, unlike codex's confirmed no-refresh or claude's
// confirmed refresh), so this does not claim the rw-copy treatment is
// unnecessary — it is the conservative default pending that verification.
// Returns ok=false when neither is available.
func resolveOpencodeContainerAuth(containerHome, _ string) (containerAuth, bool) {
	return resolveEnvOrMountAuth(
		[]string{"OPENROUTER_API_KEY"},
		opencodeAuthEnvVars,
		func() ([]Mount, bool) { return opencodeCredentialMounts(containerHome) },
	)
}

// opencodeCredentialMounts builds the read-only credential mount that
// authenticates a containerized opencode via its seeded auth.json:
// ~/.local/share/opencode/auth.json mapped into
// containerHome/.local/share/opencode/auth.json (matching opencode's own
// storage layout — see internal/opencode/capabilities.go's doc). Returns
// ok=false when the file is absent.
func opencodeCredentialMounts(containerHome string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	auth := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	if !fileExists(auth) {
		return nil, false
	}
	return []Mount{{
		Host:      auth,
		Container: filepath.Join(containerHome, ".local", "share", "opencode", "auth.json"),
		ReadOnly:  true,
	}}, true
}

// resolveAntigravityContainerAuth always reports no available auth:
// antigravity has no known container credential source yet — this stays a
// separate open workstream (stark-wheat/bare-goes) even after task
// sweet-fruit landed antigravity's IMAGE half (antigravityInstallFragment,
// profile.go's antigravity case): the agy CLI is now baked into a composed
// image, but nothing here yet knows how to authenticate it (no verified env
// var or credential-file location). It deliberately does NOT fall back to
// resolveClaudeContainerAuth: doing so was the paced-even security edge (a
// containerized antigravity run silently authenticating with the user's
// ANTHROPIC_* credentials — the wrong provider entirely). Always degrades;
// the profile's authHint explains why.
//
// PARTIALLY MEASURED (curated-scratch-home task, 2026-07-22): the premise a
// container-based fix would rely on — "a container is keyring-less, so agy
// falls back to a file-based credential path and IS isolable there" — is
// HALF confirmed and half still open. Running agy inside a plain docker
// container (no D-Bus session bus reachable) DID trip its own fallback:
// its log recorded "Using file-based token storage because no D-Bus session
// bus detected", proving the detection+fallback TRIGGER is real, not
// inferred. What remains UNVERIFIED: whether that fallback path actually
// authenticates end-to-end. The one attempt made here — mounting the host's
// existing ~/.gemini/oauth_creds.json into the container — failed ("You are
// not logged into Antigravity"), but inconclusively so: that file's
// access_token was already stale (this host's day-to-day auth has run
// through the keyring since initial login, so the file is never rewritten
// after the fact) — the failure is consistent with a stale credential, not
// necessarily with a broken fallback mechanism. Confirming the mechanism
// fully needs either a genuinely fresh file-based credential (which requires
// completing an interactive OAuth login with no keyring reachable — not
// attempted here: it needs a real browser + user consent, and was out of
// scope for a non-destructive check) or reverse-engineering the token
// refresh call, which this task deliberately did not attempt. Do not upgrade
// this from "mechanism confirmed, full success unverified" without a fresh
// measurement.
//
// RECONCILED (2026-07-22, same day, later measurement): the earlier
// unreconciled note here pointed at website/security/isolation.md's "we
// probed four ways and could not determine where its credential came from"
// container/HOME experiment, which read as a stronger, opposite result to
// this function's own container measurement above. It is not opposite. The
// keyring channel is a D-Bus Secret Service socket at a fixed,
// UID-derived path — /run/user/<uid>/bus — not something the usual
// advertising env var (DBUS_SESSION_BUS_ADDRESS) controls: the client
// library falls back to that path even when the var is unset. Reproduced
// directly: `env -i HOME=<fresh empty dir> PATH=... agy models`, with NO
// DBUS_SESSION_BUS_ADDRESS, NO XDG_RUNTIME_DIR, NO DISPLAY, and no other env
// at all, still authenticated "via keyring". So the earlier experiment's
// "keyring made unreachable" almost certainly cleared the env var — which
// removes the label, not the socket — and caught the same keyring documented
// above, not a fourth, unidentified channel. That resolves the mystery; it
// does not change this function's answer. The container measurement above
// (fresh docker container, no D-Bus session bus reachable, fallback to
// file-based token storage) still holds and is now the confirmed reason
// containers close this specific channel: fresh mount/PID namespaces mean
// /run/user/<uid>/bus does not exist inside the box, which is a kernel
// property, not vendor cooperation. Treat "runtime:container severs the
// keyring channel" as CONFIRMED. What stays open is the separate,
// lower-confidence question this comment already carried: whether a
// deliberately-seeded file-based credential would let a container
// authenticate end-to-end (see above — unverified). A distinct, also-
// unverified edge exists in the binary's own ADC-style cloud-metadata
// machinery, reachable only on a genuine cloud host or if a future
// container profile ever mounts additional cloud credentials — not
// exercised by either experiment run here.
func resolveAntigravityContainerAuth(string, string) (containerAuth, bool) {
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

// claudeCredentialCopyMounts builds the READ-WRITE credential mount that
// authenticates a subscription (OAuth) claude inside the container: the OAuth
// token file is COPIED into scratchDir and the copy is mapped into
// containerHome, read-write. A plain read-only bind of the host original (the
// prior behaviour) collides with claude's token-refresh write-back to that
// exact file — the refresh would either fail against the read-only mount or
// (worse) silently no-op, leaving a long-running container's auth to expire
// mid-session. Mounting a copy read-write instead lets the refresh land in the
// EPHEMERAL scratch copy, which is discarded at teardown along with the rest
// of the scratch root (containerWorkspace.Cleanup removes it recursively) —
// the host's own credential file is never touched, mirroring the
// copy-not-symlink rationale the host+worktree seed path already uses (this
// file's package doc above). Returns ok=false when the host OAuth token file
// is absent (nothing to copy) or the scratch copy cannot be written.
//
// tangy-heave: this used to ALSO copy+mount ~/.claude.json — not a narrow
// OAuth-association record but claude's WHOLE top-level config, including
// the host user's OWN mcpServers registrations (and whatever secrets those
// carry) on a real machine. That handed every isolated agent read access to
// the user's personal integrations — a confidentiality leak, not the
// convenience (skip re-onboarding's trust dialog) it was seeded for. Dropped
// entirely rather than filtered: filtering would mean maintaining an
// allowlist against claude's own evolving config schema, an unpins fight
// this package has no way to win, and this file's own live-verification
// (credentialSeedSpecs' claude-code entry doc) already established
// .credentials.json alone is sufficient to authenticate.
func claudeCredentialCopyMounts(containerHome, scratchDir string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	creds := filepath.Join(home, ".claude", ".credentials.json")
	if !fileExists(creds) {
		return nil, false
	}
	dir := filepath.Join(scratchDir, "claude-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	credsCopy := filepath.Join(dir, ".credentials.json")
	if err := copyCredentialFile(creds, credsCopy); err != nil {
		return nil, false
	}
	return []Mount{{
		Host:      credsCopy,
		Container: filepath.Join(containerHome, ".claude", ".credentials.json"),
		ReadOnly:  false,
	}}, true
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
	// destSubdir is the config-home subdirectory the engine's PRIMARY
	// isolation env var is pointed at by worktree.go's Env() (e.g. "claude"
	// for CLAUDE_CONFIG_DIR). The seed lands here so Env()'s wiring picks it
	// up unchanged. "" for a spec with no sourceFiles (kiro — nothing to
	// seed).
	destSubdir string
	// envTrigger is the env var whose presence means the engine already has
	// usable auth riding the process env (e.g. ANTHROPIC_API_KEY) — seeding
	// is skipped (not an error), mirroring resolveClaudeContainerAuth's
	// authEnv precedence (auth.go:82-83). "" if the engine has no such
	// bypass. Doubles as the GatedOnCreds bypass check for a
	// HonoursVarForCreds==false spec (kiro's KIRO_API_KEY): its presence is
	// what makes isolating that var SAFE rather than silently logging the
	// agent out.
	envTrigger string
	// sourceFiles returns the host credential file(s) to copy, given the
	// host home directory, in copy order. nil for a spec with no copyable
	// credential material (kiro — its creds live in a global sqlite no
	// per-agent home var relocates; see HonoursVarForCreds).
	sourceFiles func(hostHome string) []seedFile
	// HomeVars is the FULL set of isolation env vars this engine's per-agent
	// config-home contributes to worktreeWorkspace.Env() — grave-prize's
	// creds-only descriptor widened to the full config/state/creds home map
	// per the per-engine-isolation-home plan §6, so white-dawn/legal-hula's
	// var wiring rides the SAME struct as the credential seed instead of a
	// second hardcoded map. claude/codex: one entry each (their whole home
	// moves with the var). kiro: two — KIRO_HOME (sessions only, always
	// isolated) and XDG_DATA_HOME (the credential store, GatedOnCreds).
	HomeVars []homeVar
	// HonoursVarForCreds reports whether this engine's HomeVars actually
	// relocate CREDENTIALS (true — claude/codex: sourceFiles/envTrigger seed
	// them into the isolated home) or the credential store lives in a
	// GLOBAL location no HomeVar moves (false — kiro's XDG-external sqlite,
	// auth.go's resolveKiroContainerAuth doc). A false spec has no
	// sourceFiles; instead, each of its GatedOnCreds HomeVars is included in
	// Env() ONLY when envTrigger is present in the process env — absent, a
	// ClassIsolation fail-loud finding is recorded (worktree.go's
	// seedCredentials) and that var is omitted, falling back to the
	// engine's shared global store rather than silently forking it
	// per-agent. Every registry entry sets this explicitly (no useful zero
	// value).
	HonoursVarForCreds bool
}

// homeVar is one env-var-to-subdir mapping an engine's isolation home
// contributes to worktreeWorkspace.Env(). Subdir is joined under the
// per-agent configHome (e.g. "claude" → CLAUDE_CONFIG_DIR=<configHome>/claude).
// codex's Subdir is ".codex" (dot-prefixed) so codex's OWN
// cellScopedCodexHome join (which appends "/.codex" to a project-dir-shaped
// value) lands on this EXACT directory when the isolation-provided configHome
// is treated as that virtual project dir — see internal/codex/backend.go's
// resolveCodexHome, which is the single place this convention is documented
// and relied on.
type homeVar struct {
	EnvVar string
	Subdir string
	// GatedOnCreds marks this var as the one that relocates the engine's
	// CREDENTIAL store (not just config/session state) — kiro's
	// XDG_DATA_HOME (its KIRO_HOME entry is NOT gated: it relocates sessions
	// only, no creds, so it isolates unconditionally). Only meaningful on a
	// HonoursVarForCreds==false spec; ignored otherwise (a
	// HonoursVarForCreds==true spec's vars are never gated — a seed
	// failure there is reported by seedCredentials, but the var still
	// isolates so the agent gets an isolated-but-possibly-unseeded home
	// rather than silently sharing the global one).
	GatedOnCreds bool
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
// which the same keys already drive). It is now the SINGLE per-engine
// isolation-home descriptor (per-engine-isolation-home plan §6): every
// entry's HomeVars drives worktreeWorkspace.Env() in addition to whatever
// credential-seed behaviour HonoursVarForCreds selects. antigravity has NO
// entry here — but NOT because it has no lever at all (that claim was
// FALSE and has been corrected): agy reads $HOME directly for its whole
// config/session-state tree — a full .gemini/ tree DOES relocate with it,
// measured by chmod-000-crashes-agy on a fake HOME. What it does NOT
// relocate is CREDENTIALS: agy authenticates through an OS-session-scoped
// D-Bus Secret Service keyring that HOME does not gate at all (measured
// with `env -i` + an empty fake HOME — still authenticated via keyring).
// credentialSeedSpecs exists to copy CREDENTIAL material into a scoped
// subdir a HonoursVarForCreds engine's isolation var points at; antigravity
// has no such var (HOME itself is the only lever, and HOME does not carry
// credentials), so it does not fit this registry's shape at all — it is
// handled by the SEPARATE curatedHomeSpecs registry (curatedhome.go) instead,
// which overrides HOME wholesale and is explicit that only config/session
// state — never auth — moves with it.
//
//   - claude: HonoursVarForCreds true — CLAUDE_CONFIG_DIR relocates both
//     config AND credentials, so seeding copies .credentials.json (+
//     .claude.json) into it.
//   - codex: HonoursVarForCreds true — CODEX_HOME relocates config, state,
//     AND credentials (auth.json resolves from $CODEX_HOME only — see
//     internal/codex/backend.go's package doc). This registry is now codex's
//     ONE credential-seed mechanism: codex's prior seed path
//     (linkUserCodexAuth, a SYMLINK into a cell-scoped
//     <WorkDir>/.codex — a DIFFERENT directory than this package's
//     configHome) is deleted (balmy-comic) in favour of this COPY, and
//     internal/codex/backend.go's resolveCodexHome makes the isolation-
//     provided CODEX_HOME (this spec's HomeVars entry) the single owner for
//     an isolated run, resolving the two-mechanism conflict grave-prize's
//     own doc used to warn about here. destSubdir/HomeVars both use ".codex"
//     (dot-prefixed) — see homeVar's doc for why the leaf name matters.
//   - kiro: HonoursVarForCreds FALSE — subscription auth lives in a GLOBAL
//     sqlite under $XDG_DATA_HOME regardless of KIRO_HOME (see
//     resolveKiroContainerAuth's doc, verified live against kiro-cli 2.12.1),
//     so there is no per-agent file to copy (sourceFiles/destSubdir stay
//     unset). Its XDG_DATA_HOME HomeVar is GatedOnCreds: worktree.go's
//     seedCredentials includes it in Env() only when KIRO_API_KEY is set
//     (live-verified: a fresh XDG_DATA_HOME + KIRO_API_KEY authenticates
//     headlessly, no browser), and records a ClassIsolation fail-loud
//     finding + omits it otherwise — turning kiro's previously-SILENT
//     shared-sqlite non-isolation (legal-hula) into a real per-agent
//     isolation on the KIRO_API_KEY path and a loud, degradable error
//     otherwise. Its KIRO_HOME entry (session jsonl only, no creds) stays
//     unconditional.
var credentialSeedSpecs = map[string]credentialSeedSpec{
	"claude-code": {
		engine:     "claude",
		destSubdir: "claude",
		envTrigger: "ANTHROPIC_API_KEY",
		sourceFiles: func(hostHome string) []seedFile {
			// tangy-heave: ~/.claude.json used to be seeded here too
			// (optional, "carries over account association/trust-dialog
			// state instead of re-onboarding"). Dropped: on a real host
			// that file is claude's WHOLE top-level config, including the
			// user's OWN mcpServers registrations — seeding it handed
			// every isolated agent read access to the user's personal
			// integrations (and whatever secrets they carry), a
			// confidentiality leak this package must not reproduce for
			// mere onboarding convenience. Live-verified (2026-07, claude
			// 2.1.210): claude auto-creates its own .claude.json inside
			// CLAUDE_CONFIG_DIR when the var is set and none exists there
			// yet, so .credentials.json alone remains sufficient to
			// authenticate — nothing here needs .claude.json's contents
			// at all.
			return []seedFile{
				{
					host:     filepath.Join(hostHome, ".claude", ".credentials.json"),
					destName: ".credentials.json",
					required: true,
				},
			}
		},
		HomeVars:           []homeVar{{EnvVar: "CLAUDE_CONFIG_DIR", Subdir: "claude"}},
		HonoursVarForCreds: true,
	},
	"codex": {
		engine:     "codex",
		destSubdir: ".codex",
		envTrigger: "OPENAI_API_KEY", // live-verified: `codex doctor` reports "auth is provided by environment" with a fresh CODEX_HOME + OPENAI_API_KEY set, no `codex login` needed.
		sourceFiles: func(hostHome string) []seedFile {
			return []seedFile{
				{
					// "auth.json" duplicates internal/codex/backend.go's
					// codexAuthFile literal — not imported to avoid a
					// cross-package dependency from this generic seed
					// registry onto one specific engine package.
					host:     filepath.Join(hostHome, ".codex", "auth.json"),
					destName: "auth.json",
					required: true,
				},
			}
		},
		HomeVars:           []homeVar{{EnvVar: "CODEX_HOME", Subdir: ".codex"}},
		HonoursVarForCreds: true,
	},
	"kiro": {
		engine:     "kiro",
		envTrigger: "KIRO_API_KEY",
		HomeVars: []homeVar{
			{EnvVar: "KIRO_HOME", Subdir: "kiro"},
			{EnvVar: "XDG_DATA_HOME", Subdir: "xdg-data", GatedOnCreds: true},
		},
		HonoursVarForCreds: false,
	},
}

// SeedCodexHome seeds destDir/.codex (a codex "virtual project dir" —
// cellScopedCodexHome joins ".codex" onto it, see
// internal/codex/backend.go's resolveCodexProjectDir) with the host's codex
// credentials, for a caller whose CODEX_HOME relocation is NOT driven by an
// isolation Policy at all: codex ALWAYS relocates CODEX_HOME to
// <WorkDir>/.codex even under the plain None/host axis (its own in-tree
// fallback) — a relocated home this package's Policy machinery never sees
// or seeds, so it starts empty and codex 401s silently (warm-yodel).
//
// This reuses the EXACT SAME copy-based credentialSeedSpecs["codex"]
// descriptor and hostCredentialSeed mechanics worktree.go's
// provisionConfigHome already uses for the fan-out path — NOT a second
// seeding mechanism — exported here because internal/codex owns codex's own
// relocation policy but not this package's credential registry (an import
// from internal/codex onto this package is safe: this package imports
// neither internal/codex nor internal/shared/agent).
//
// Returns (skipped=true, nil) when OPENAI_API_KEY already authenticates
// (codex's envTrigger) — nothing to copy, not an error. Returns a
// descriptive, actionable error when the host source (~/.codex/auth.json)
// is missing or the copy itself fails, so the caller can fail loud instead
// of silently launching an unauthenticated codex.
func SeedCodexHome(destDir string) (skipped bool, err error) {
	spec, ok := credentialSeedSpecs["codex"]
	if !ok || spec.sourceFiles == nil {
		return false, fmt.Errorf("codex credential seed spec is not registered (internal error)")
	}
	result, err := hostCredentialSeed(spec, destDir)
	if err != nil {
		return false, err
	}
	if result == seedNoSource {
		return false, fmt.Errorf("no OPENAI_API_KEY and no host ~/.codex/auth.json credentials found to authenticate this run — run `codex login` or set OPENAI_API_KEY (or pass --degraded)")
	}
	return result == seedSkippedEnv, nil
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
