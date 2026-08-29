package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	// non-interactive mode never refreshes (codex/opencode), read-WRITE and
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
// container: the scoped env vars to inject (env passthrough) and/or the read-only
// credential mounts to bind into the fresh HOME (subscription OAuth). Each engine
// resolves its own plan behind the engineContainerSpec.resolveAuth seam (claude:
// ANTHROPIC_* passthrough or ~/.claude mounts; kiro: KIRO_API_KEY passthrough).
// Only the TRUSTED top-level run reaches it — low-trust fan-out auth
// (budget-capped per-agent keys, T1.5) is a separate, later concern.
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

// kiroAuthEnvVars is the SCOPED set of Kiro auth vars a kiro run honors — the
// only host env allowed to cross into the container for kiro auth. KIRO_API_KEY
// presence is the TRIGGER (Kiro's headless mode skips the browser login when it
// is set); the AWS_* vars ride ALONG when present (a Bedrock-backed kiro
// configuration reads them like any AWS SDK client) but are deliberately NOT a
// standalone trigger of their own yet — whether AWS credentials alone (no
// KIRO_API_KEY) authenticate a containerized kiro needs a live kiro
// verification this package cannot run hermetically; treating them as a
// trigger without that confirmation risks launching a container that starts but
// never actually authenticates, a NEW silent-no-op. AWS_PROFILE alone is
// forwarded but is likely insufficient on its own: spec-based auth resolves
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
// whose fresh HOME is containerHome. It PREFERS env passthrough
// (OPENAI_API_KEY) and otherwise falls back to mounting the host's
// ~/.codex/auth.json READ-ONLY into the container HOME (codexCredentialMounts).
// Unlike claude's subscription refresh, the read-only mount is SAFE here:
// `codex exec`/non-interactive mode never refreshes the ChatGPT auth token in
// place, so there is no write-back to collide with — codex does
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
// location the host+worktree credential seed path
// (hostCredentialSeed below) already knows — instead of hard-coding the path a
// second time, per the plan's "one source-discovery, not two"
// (container-image-auth-delivery.plan.md §2.2). Returns ok=false when the
// file is absent or the codex spec is missing/has no sourceFiles (defensive;
// should never happen — codex's spec always sets both).
func codexCredentialMounts(containerHome string) ([]Mount, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return nil, false
	}
	spec, ok := credentialSeedSpecFor("codex")
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
// to authenticate (credentialSeedSpecs' claude-code entry doc). Returns
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
	// loginHint is the command that MAKES this engine's credential file
	// exist (e.g. "claude login") — the one fix, besides envTrigger, that
	// resolves the "nothing seedable" fail-loud case. It lives on the spec
	// rather than inside a per-engine error string so
	// CopyAmbient's one message covers every engine and no engine can be
	// added with a fail-loud path that names no fix at all. "" for a spec
	// with no sourceFiles (kiro — nothing to log in FOR, here).
	loginHint string
	// HomeVars is the FULL set of isolation env vars this engine's per-agent
	// config-home contributes to worktreeWorkspace.Env() — the creds-only
	// descriptor widened to the full config/state/creds home map per the
	// per-engine-isolation-home plan §6, so the var wiring rides the SAME
	// struct as the credential seed instead of a
	// second hardcoded map. claude/codex: one entry each (their whole home
	// moves with the var). kiro: two — KIRO_HOME (kiro's WHOLE home except
	// credentials, always isolated) and XDG_DATA_HOME (the credential store,
	// GatedOnCreds).
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
	// XDG_DATA_HOME (its KIRO_HOME entry is NOT gated: it relocates kiro's
	// whole home but no creds, so it isolates unconditionally). Only meaningful on a
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
// "claude-code", "codex", "kiro" — see enginespec.go's engineContainerSpecFor,
// which the same keys already drive). It is now the SINGLE per-engine
// isolation-home descriptor (per-engine-isolation-home plan §6): every
// entry's HomeVars drives worktreeWorkspace.Env() in addition to whatever
// credential-seed behaviour HonoursVarForCreds selects. A backend absent from
// this registry has no known host isolation lever at all and keeps the
// pre-fix, config-only-isolation no-op.
//
// Keyed by CANONICAL name: enginekeys.go's init asserts it, and
// credentialSeedSpecFor is the only read path, so an aliased spelling cannot
// miss an engine that is in fact registered here.
//
//   - claude: HonoursVarForCreds true — CLAUDE_CONFIG_DIR relocates both
//     config AND credentials, so seeding copies .credentials.json (+
//     .claude.json) into it.
//
//   - codex: HonoursVarForCreds true — CODEX_HOME relocates config, state,
//     AND credentials (auth.json resolves from $CODEX_HOME only — see
//     internal/codex/backend.go's package doc). This registry is now codex's
//     ONE credential-seed mechanism: codex's prior seed path
//     (linkUserCodexAuth, a SYMLINK into a cell-scoped
//     <WorkDir>/.codex — a DIFFERENT directory than this package's
//     configHome) is deleted in favour of this COPY, and
//     internal/codex/backend.go's resolveCodexHome makes the isolation-
//     provided CODEX_HOME (this spec's HomeVars entry) the single owner for
//     an isolated run, resolving the two-mechanism conflict this package's
//     own doc used to warn about here. destSubdir/HomeVars both use ".codex"
//     (dot-prefixed) — see homeVar's doc for why the leaf name matters.
//
//   - kiro: HonoursVarForCreds FALSE — subscription auth lives in a GLOBAL
//     sqlite under $XDG_DATA_HOME regardless of KIRO_HOME (see
//     resolveKiroContainerAuth's doc, verified live against kiro-cli 2.12.1),
//     so there is no per-agent file to copy (sourceFiles/destSubdir stay
//     unset). Its XDG_DATA_HOME HomeVar is GatedOnCreds: worktree.go's
//     seedCredentials includes it in Env() only when KIRO_API_KEY is set
//     (live-verified: a fresh XDG_DATA_HOME + KIRO_API_KEY authenticates
//     headlessly, no browser), and records a ClassIsolation fail-loud
//     finding + omits it otherwise — turning kiro's previously-SILENT
//     shared-sqlite non-isolation into a real per-agent
//     isolation on the KIRO_API_KEY path and a loud, degradable error
//     otherwise. Its KIRO_HOME entry stays unconditional — it carries no
//     creds, so isolating it can never log an agent out.
//
//     KIRO_HOME's SCOPE, corrected: this file used to describe it
//     as relocating "sessions only" / "session jsonl only". That was true of
//     an older kiro-cli and is now wrong — since kiro-cli 2.3.0 KIRO_HOME
//     relocates the FULL home: global agents, prompts, skills, steering,
//     settings AND sessions (internal/kiro.HomeEnv's doc). The credential
//     carve-out is the only part that still holds, and it is the only part
//     this comment ever needed to make. The correction matters beyond
//     accuracy: "sessions only" made pointing an in-tree AGENT run at the
//     human's own ~/.kiro look harmless, when it in fact hands that agent the
//     human's global agents and steering — see internal/kiro.SessionHome.
//
//   - opencode: HonoursVarForCreds TRUE — the OPPOSITE of
//     kiro's shape immediately above, despite both engines relocating via
//     XDG_DATA_HOME: opencode's auth.json genuinely lives under
//     $XDG_DATA_HOME/opencode (live-verified against opencode 1.18.1 — see
//     the entry's own doc for the full measurement), so it is seeded via
//     sourceFiles/destSubdir like claude/codex, never gated. Two HomeVars —
//     XDG_CONFIG_HOME (config only) and XDG_DATA_HOME (config + creds,
//     destSubdir "xdg-data/opencode" NESTED one level under the HomeVar's
//     own "xdg-data" Subdir, because opencode itself appends "/opencode"
//     onto XDG_DATA_HOME rather than owning the var outright the way
//     CLAUDE_CONFIG_DIR/CODEX_HOME do). Before this entry existed,
//     "opencode" had NO registry entry at all — worktree.go's
//     seedCredentials short-circuited at `if !ok { return nil }` with no
//     ClassIsolation finding, a SILENT no-op strictly worse than kiro's loud
//     "nothing seedable" handling.
// parked_engines: the codex/kiro/opencode entries are commented out — those
// backends are unregistered in internal/lm/backends/registry.go for the
// duration of the delivery-interface refactor, so nothing calls this
// registry with those names today. tests/arch's
// testMockConfigHomeEnvKeysRoster compares this registry's HomeVars against
// backends.ConfigHomeEnvKeys(), which narrows the same way (mock.go) — both
// sides must move together. grep -rn parked_engines finds every parked site.
var credentialSeedSpecs = map[string]credentialSeedSpec{
	"claude-code": {
		engine:     "claude",
		destSubdir: "claude",
		envTrigger: "ANTHROPIC_API_KEY",
		loginHint:  "claude login",
		sourceFiles: func(hostHome string) []seedFile {
			// ~/.claude.json used to be seeded here too
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
// 	"codex": {
// 		engine:     "codex",
// 		destSubdir: ".codex",
// 		envTrigger: "OPENAI_API_KEY", // live-verified: `codex doctor` reports "auth is provided by environment" with a fresh CODEX_HOME + OPENAI_API_KEY set, no `codex login` needed.
// 		loginHint:  "codex login",
// 		sourceFiles: func(hostHome string) []seedFile {
// 			return []seedFile{
// 				{
// 					// "auth.json" duplicates internal/codex/backend.go's
// 					// codexAuthFile literal — not imported to avoid a
// 					// cross-package dependency from this generic seed
// 					// registry onto one specific engine package.
// 					host:     filepath.Join(hostHome, ".codex", "auth.json"),
// 					destName: "auth.json",
// 					required: true,
// 				},
// 			}
// 		},
// 		HomeVars:           []homeVar{{EnvVar: "CODEX_HOME", Subdir: ".codex"}},
// 		HonoursVarForCreds: true,
// 	},
// 	"kiro": {
// 		engine:     "kiro",
// 		envTrigger: "KIRO_API_KEY",
// 		HomeVars: []homeVar{
// 			{EnvVar: "KIRO_HOME", Subdir: "kiro"},
// 			{EnvVar: "XDG_DATA_HOME", Subdir: "xdg-data", GatedOnCreds: true},
// 		},
// 		HonoursVarForCreds: false,
// 	},
// 	// opencode: HonoursVarForCreds TRUE — UNLIKE kiro. This is the opposite
// 	// shape from kiro's entry immediately above: opencode's XDG_DATA_HOME
// 	// genuinely relocates its credential file, not a global unrelocatable
// 	// store, so it seeds via sourceFiles/destSubdir exactly like claude/codex
// 	// rather than gating via GatedOnCreds (see
// 	// resolveOpencodeContainerAuth's doc for the container-axis half of this
// 	// same engine's auth story).
// 	//
// 	// MEASURED against opencode 1.18.1, not vendor-documented: opencode's
// 	// docs cover only OPENCODE_CONFIG/OPENCODE_CONFIG_DIR — XDG_DATA_HOME,
// 	// the resulting auth.json location, and OPENCODE_DB appear NOWHERE in its
// 	// docs. Confirmed by direct interrogation of the compiled binary
// 	// (`process.env.XDG_DATA_HOME`, read directly with a `~/.local/share`
// 	// fallback, then joined with "opencode" then "auth.json") and,
// 	// decisively, live: `opencode auth list` under `env -i` with a fresh
// 	// scratch HOME and all four XDG_* vars pointed at a seeded auth.json
// 	// printed the RELOCATED path and "1 credentials" (the OpenRouter entry
// 	// copied in) — the same command against an EMPTY scratch XDG_DATA_HOME
// 	// printed "0 credentials", never silently falling back to the real host
// 	// key. This is an implementation detail that can drift silently on
// 	// upgrade (this project has been burned by that exact failure mode
// 	// before — see kiro's entry above) — RE-VERIFY against any opencode
// 	// version bump past 1.18.1 before trusting this comment.
// 	//
// 	// destSubdir is NESTED ("xdg-data/opencode"), unlike claude/codex's flat
// 	// destSubdir that equals their one HomeVar's Subdir directly: opencode
// 	// itself appends "/opencode" onto whatever XDG_DATA_HOME resolves to (it
// 	// is a shared per-app-name XDG root, not a ctxloom-owned directory the
// 	// way CLAUDE_CONFIG_DIR/CODEX_HOME are), so the seeded file must land one
// 	// level deeper than the HomeVar's own Subdir ("xdg-data") for opencode's
// 	// OWN join to land on it. XDG_CONFIG_HOME rides along as a second
// 	// HomeVar for config-only isolation (opencode.json/opencode.jsonc read
// 	// from Path.config, same OPENCODE_CONFIG_DIR-or-XDG_CONFIG_HOME
// 	// precedence) — it never carries credentials, but leaving it unisolated
// 	// while XDG_DATA_HOME isolates would be a needless half-measure now that
// 	// the mechanism is known to work.
// 	//
// 	// mcp-auth.json (per-MCP-server OAuth tokens) rides along as an OPTIONAL
// 	// sourceFiles entry: confirmed present in the 1.18.1 binary at the exact
// 	// same Path.data-joined location as auth.json, but not every user has
// 	// configured an OAuth-authenticated MCP server, so its absence must never
// 	// block seeding the credential that actually matters.
// 	//
// 	// NOT used here: OPENCODE_TEST_HOME, also present in the binary — it is
// 	// explicitly a TEST-only hook (its own name says so), not a production
// 	// isolation lever; using it here would be relying on unstable internal
// 	// test scaffolding rather than the real, user-facing XDG contract this
// 	// entry is built on. Also unresolved and NOT claimed either way: whether
// 	// a non-interactive opencode run refreshes auth.json in place
// 	// (opencodeCredentialMounts above already flags this as unverified for
// 	// the container mount path); this worktree seed COPIES into a writable
// 	// per-agent directory, so a refresh there would land in the copy rather
// 	// than colliding with a read-only mount the way claude's container path
// 	// had to guard against — but whether opencode ever performs such a
// 	// refresh at all remains unmeasured, and this entry does not resolve
// 	// that question.
// 	"opencode": {
// 		engine:     "opencode",
// 		destSubdir: filepath.Join("xdg-data", "opencode"),
// 		envTrigger: "OPENROUTER_API_KEY", // mirrors resolveOpencodeContainerAuth's container-axis trigger
// 		loginHint:  "opencode auth login",
// 		sourceFiles: func(hostHome string) []seedFile {
// 			dir := filepath.Join(hostHome, ".local", "share", "opencode")
// 			return []seedFile{
// 				{
// 					host:     filepath.Join(dir, "auth.json"),
// 					destName: "auth.json",
// 					required: true,
// 				},
// 				{
// 					host:     filepath.Join(dir, "mcp-auth.json"),
// 					destName: "mcp-auth.json",
// 					required: false,
// 				},
// 			}
// 		},
// 		HomeVars: []homeVar{
// 			{EnvVar: "XDG_CONFIG_HOME", Subdir: "xdg-config"},
// 			{EnvVar: "XDG_DATA_HOME", Subdir: "xdg-data"},
// 		},
// 		HonoursVarForCreds: true,
// 	},
}

// CredentialSeedEngineNames returns the backend names credentialSeedSpecs
// covers (one of the four independently-maintained engine-identity
// rosters found spread across the codebase — see
// tests/arch/engine_identity_arch_test.go's TestArch_EngineIdentityRosters_
// MembersAreRegisteredBackends, which validates every name returned here is
// still a real, currently-registered internal/lm/backends name). Exported
// read-only so that gate can reach this package's otherwise-unexported
// roster without isolation importing backends (which would cycle: backends
// already imports isolation).
func CredentialSeedEngineNames() []string {
	names := make([]string, 0, len(credentialSeedSpecs))
	for name := range credentialSeedSpecs {
		names = append(names, name)
	}
	return names
}

// CredentialSeedHomeVar is a read-only copy of one homeVar entry, exported so
// tests/arch's engine-layout gate can check credentialSeedSpecs' env-var-name
// and subdir literals against the owning
// engine package's own exported constants. This package cannot import
// claude/codex/kiro/opencode in production (each of them imports
// internal/acp, which imports this package — a real cycle), so those tables
// keep their literals; the arch test, which is free to import every package,
// is the enforcement point instead.
type CredentialSeedHomeVar struct {
	EnvVar       string
	Subdir       string
	GatedOnCreds bool
}

// CredentialSeedHomeVars returns engine's HomeVars (nil for an unregistered
// engine name — see CredentialSeedEngineNames for the valid keys).
func CredentialSeedHomeVars(engine string) []CredentialSeedHomeVar {
	spec, ok := credentialSeedSpecFor(engine)
	if !ok {
		return nil
	}
	out := make([]CredentialSeedHomeVar, len(spec.HomeVars))
	for i, hv := range spec.HomeVars {
		out[i] = CredentialSeedHomeVar(hv)
	}
	return out
}

// CredentialSeedDestSubdir returns engine's destSubdir and true, or ("",
// false) for an unregistered engine name.
func CredentialSeedDestSubdir(engine string) (string, bool) {
	spec, ok := credentialSeedSpecFor(engine)
	if !ok {
		return "", false
	}
	return spec.destSubdir, true
}

// CredentialSeedFile is a read-only copy of one seedFile entry, with Host
// resolved against a fixed sentinel HOME (never a real filesystem path) and
// reduced to its slash-separated path relative to that sentinel, so the arch
// gate can check the engine-owned directory COMPONENT of the source path
// without either touching a real filesystem or hard-coding a HOME value of
// its own.
type CredentialSeedFile struct {
	// HostRelToHome is the source path's slash-separated component(s) after
	// $HOME — e.g. ".codex/auth.json" or ".local/share/opencode/auth.json".
	HostRelToHome string
	DestName      string
	Required      bool
}

// credentialSeedSourceFileSentinelHome is the fixed stand-in HOME
// CredentialSeedSourceFiles resolves each spec's sourceFiles function
// against. sourceFiles only ever filepath.Joins onto it (no I/O), so any
// fixed value works; using an obviously-fake one makes a future sourceFiles
// implementation that DID touch the filesystem fail loudly instead of
// silently reading the real host's files during a test run.
const credentialSeedSourceFileSentinelHome = "/sentinel-home-never-real"

// CredentialSeedSourceFiles returns engine's seed-file facts (nil when the
// engine has no sourceFiles — e.g. kiro, whose credentials do not relocate
// via a seeded file at all).
func CredentialSeedSourceFiles(engine string) []CredentialSeedFile {
	spec, ok := credentialSeedSpecFor(engine)
	if !ok || spec.sourceFiles == nil {
		return nil
	}
	files := spec.sourceFiles(credentialSeedSourceFileSentinelHome)
	out := make([]CredentialSeedFile, len(files))
	for i, f := range files {
		rel, err := filepath.Rel(credentialSeedSourceFileSentinelHome, f.host)
		if err != nil {
			rel = f.host
		}
		out[i] = CredentialSeedFile{HostRelToHome: filepath.ToSlash(rel), DestName: f.destName, Required: f.required}
	}
	return out
}

// CopyAmbient (ambient.go) is what REPLACED the two exported per-engine
// preparers that used to live here, PrepareCodexHome and PrepareClaudeHome.
// They were the same function twice — the same credentialSeedSpecs descriptor,
// the same hostCredentialSeed mechanics, differing only in a map key and an
// error string — and neither could carry the working directory the engine
// write-config directive needs to generate a workspace-trust answer. One named
// mechanism, one allow-list per engine, both axes through it.

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
func hostCredentialSeed(spec credentialSeedSpec, configHome string, projector agent.CredentialProjector) (seedResult, error) {
	if spec.envTrigger != "" && os.Getenv(spec.envTrigger) != "" {
		return seedSkippedEnv, nil
	}
	files, ok := hostSeedSources(spec)
	if !ok {
		return seedNoSource, nil
	}
	destDir := filepath.Join(configHome, spec.destSubdir)
	if err := prepareSeedDir(spec, destDir); err != nil {
		return seedNoSource, err
	}
	return copySeedFiles(spec, files, destDir, projector)
}

// hostSeedSources resolves spec's host source files against the host HOME and
// reports whether there is anything seedable at all. ok=false is the
// "nothing to seed" degrade — an unresolvable/empty host HOME, or an absent
// REQUIRED file — never an error, because the caller must be free to proceed
// (worktree.go turns it into its own fail-loud decision).
func hostSeedSources(spec credentialSeedSpec) ([]seedFile, bool) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		// Still a degrade, never an abort (the caller must not be blocked by a
		// HOME lookup). But an unresolvable host HOME is an ENVIRONMENT FAULT,
		// not the ordinary "this host has no credential file" the caller's own
		// message describes — leaving it silent surfaced a real fault as advice
		// to run `claude login`/`codex login`, which cannot help. Same handling
		// provisionCuratedHome gives the identical failure.
		clidiag.Warn("ctxloom",
			"%s credential seed: could not resolve the host HOME to copy credentials from (%v); this run is treated as having no host credentials to seed",
			spec.engine, err)
		return nil, false
	}
	files := spec.sourceFiles(home)
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
func prepareSeedDir(spec credentialSeedSpec, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create %s credential seed dir: %w", spec.engine, err)
	}
	if err := os.Chmod(destDir, 0o700); err != nil {
		return fmt.Errorf("restrict %s credential seed dir: %w", spec.engine, err)
	}
	return nil
}

// copySeedFiles copies each PRESENT source file into destDir (required ones are
// already known present — see hostSeedSources). A copy failure is an error, not
// a degrade: the material exists and we failed to place it. Copying nothing
// reports seedNoSource, never seedOK — no current spec reaches that (claude's
// primary file is required), but a future all-optional spec must not report
// success having delivered zero bytes.
func copySeedFiles(spec credentialSeedSpec, files []seedFile, destDir string, projector agent.CredentialProjector) (seedResult, error) {
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
			return seedNoSource, fmt.Errorf("seed %s credential %q: %w", spec.engine, f.destName, err)
		}
		seededAny = true
	}
	if !seededAny {
		return seedNoSource, nil
	}
	return seedOK, nil
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
	// unvalidated destination — e.g. a repo-tracked `.codex/auth.json` symlink
	// pointing at the real `~/.codex/auth.json` — turns this seed into an
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
