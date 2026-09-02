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
// container: the scoped env vars to inject (env passthrough) and/or the read-only
// credential mounts to bind into the fresh HOME (subscription OAuth). Each engine
// resolves its own plan behind the engineContainerSpec.resolveAuth seam (claude:
// ANTHROPIC_* passthrough or ~/.claude mounts).
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
// engine's config lookup via an env var (CLAUDE_CONFIG_DIR, see
// worktree.go's Env()) pointing at a per-agent scratch dir
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
	// up unchanged. "" for a spec with no sourceFiles (nothing to seed).
	destSubdir string
	// envTrigger is the env var whose presence means the engine already has
	// usable auth riding the process env (e.g. ANTHROPIC_API_KEY) — seeding
	// is skipped (not an error), mirroring resolveClaudeContainerAuth's
	// authEnv precedence (auth.go:82-83). "" if the engine has no such
	// bypass. Doubles as the GatedOnCreds bypass check for a
	// HonoursVarForCreds==false spec: its presence is what makes isolating
	// that var SAFE rather than silently logging the agent out.
	envTrigger string
	// sourceFiles returns the host credential file(s) to copy, given the
	// host home directory, in copy order. nil for a spec with no copyable
	// credential material — creds that live in a global store no per-agent
	// home var relocates; see HonoursVarForCreds.
	sourceFiles func(hostHome string) []seedFile
	// loginHint is the command that MAKES this engine's credential file
	// exist (e.g. "claude login") — the one fix, besides envTrigger, that
	// resolves the "nothing seedable" fail-loud case. It lives on the spec
	// rather than inside a per-engine error string so
	// CopyAmbient's one message covers every engine and no engine can be
	// added with a fail-loud path that names no fix at all. "" for a spec
	// with no sourceFiles (nothing to log in FOR, here).
	loginHint string
	// HomeVars is the FULL set of isolation env vars this engine's per-agent
	// config-home contributes to worktreeWorkspace.Env() — the creds-only
	// descriptor widened to the full config/state/creds home map per the
	// per-engine-isolation-home plan §6, so the var wiring rides the SAME
	// struct as the credential seed instead of a
	// second hardcoded map. An engine whose whole home moves with a single
	// var has one entry; an engine that splits config and data across
	// separate XDG vars contributes one entry per var.
	HomeVars []homeVar
	// HonoursVarForCreds reports whether this engine's HomeVars actually
	// relocate CREDENTIALS (true — sourceFiles/envTrigger seed them into the
	// isolated home) or the credential store lives in a GLOBAL location no
	// HomeVar moves (false). A false spec has no
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
//
// The LEAF NAME is load-bearing, not cosmetic: an engine that composes its own
// home path from a project-dir-shaped value must land on this EXACT directory,
// so a Subdir here has to match whatever leaf that engine's own resolution
// appends.
type homeVar struct {
	EnvVar string
	Subdir string
	// GatedOnCreds marks this var as the one that relocates the engine's
	// CREDENTIAL store (not just config/session state). A var that relocates
	// an engine's home but no credentials is NOT gated: it isolates
	// unconditionally. Only meaningful on a
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
// consults, keyed by the REGISTERED backend name (internal/lm/backends — see
// enginespec.go's engineContainerSpecFor, which the same keys already drive).
// It is the SINGLE per-engine
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
// engine package's own exported constants. This package does not import the
// engine packages, so those tables keep their literals; the arch test, which
// is free to import every package, is the enforcement point instead.
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
	// $HOME — e.g. ".claude/.credentials.json".
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
// engine has no sourceFiles — credentials that do not relocate via a seeded
// file at all).
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
// preparers that used to live here, one per engine.
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
		// to run `claude login`, which cannot help. Same handling
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
