package codex

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// AuthFileName is the credential file codex reads from $CODEX_HOME — the
// SAME literal internal/lm/isolation/auth.go's credentialSeedSpecs registry
// and codexCredentialMounts use for the host-seed and container-mount
// destinations respectively. Exported so tests/arch's engine-layout gate
// (TestArch_EngineLayoutAgreement) can check those isolation-owned literals
// against this package's own fact instead of the two staying independently
// hand-maintained; isolation itself still cannot import this package in
// production (codex -> internal/acp -> internal/lm/isolation is a real
// cycle), so its copy of "auth.json" stays a literal.
const AuthFileName = "auth.json"

// backend.go wires codex onto the shared launch core and the surfaces × cells
// delivery seam.

// CodexConfig is codex's typed LLM config. The backend owns this struct; the
// config package only carries the raw body that decodes into it.
type CodexConfig struct {
	// Model is intentionally NOT a field here: it would be decoded
	// and read by nothing — the model actually reaches the run through
	// config.Body["model"] (config.ResolveLLM), and mapstructure ignores an
	// unmatched "model" key in the raw body without complaint.
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
	// Thinking is the normalized reasoning/thinking-budget level
	// (off|low|medium|high — agent.ThinkingLevel). Empty or unrecognized
	// defaults to "medium". See chat.go's translation to codex-acp's
	// model_reasoning_effort/model_reasoning_summary config keys. Codex is
	// model-gated (model_supports_reasoning_summaries) and returns no
	// reasoning at all on ChatGPT-OAuth auth (`codex doctor`: reasoning
	// header false) — an account-tier limit this knob cannot lift.
	Thinking string `mapstructure:"thinking"`
}

// BackendType identifies the backend this config drives.
func (CodexConfig) BackendType() string { return "codex" }

// GetEnv returns the labeled entry's env map. Lets shared code (see
// operations.LLMEnvFor) reach a decoded config's Env through an interface
// assertion instead of a concrete-type switch — internal/operations may not
// import engine plugin packages directly (ADR-0026).
func (c CodexConfig) GetEnv() map[string]string { return c.Env }

// Codex implements the Backend interface for OpenAI Codex CLI. The shared launch
// core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; Codex adds only the Codex-specific Configure/Execute.
type Codex struct {
	agent.LaunchBackend
	// resolvedProjectDir is the "virtual project dir" cellScopedCodexHome joins
	// ".codex" onto for THIS run — computed once per Setup
	// and read by both buildSurfaces (Setup's delivery target) and
	// cellCodexHomeEnv (Execute's env), so the two can never disagree about
	// where CODEX_HOME points. See resolveCodexProjectDir.
	resolvedProjectDir string
	// resolvedTrustAbsPath is the absolute WorkDir to pre-seed
	// `[projects."<path>"] trust_level = "trusted"` for. Set on ALL THREE axes
	// resolveCodexProjectDir can land on, because each of them points codex at
	// a home CTXLOOM provisioned rather than one codex built up itself: the
	// isolation-provided CODEX_HOME, a container cell's fresh $HOME, and — since
	// the engine-home policy moved it out of the project root — the in-tree
	// state home. None is committed, so a machine-specific absolute path baked
	// into one is harmless. See docs/trust-model.md.
	resolvedTrustAbsPath string
	// credentialErr is set by Setup (ensureCodexCredentials) when this run's
	// CODEX_HOME has no usable codex credentials AND Setup could not seed/
	// verify them, on either axis (in-tree host run / container fresh-$HOME).
	// Execute checks it FIRST and refuses to launch codex at
	// all when set, rather than the historical silent 401/exit-0: Setup's own
	// errors are fault-tolerantly warned-and-ignored by the grpc server (see
	// its doc), so a credential failure can only surface loudly from Execute.
	// nil on the isolation-provided axis (worktree fan-out) — that axis is
	// already seeded before this run starts and already fails loud earlier,
	// at isolation.Prepare's own choke gate (see ensureCodexCredentials).
	credentialErr error
	// thinking is the resolved normalized reasoning level (Configure defaults
	// it to agent.ThinkingMedium — the zero value, so an unconfigured backend
	// is still medium). Chat translates it into codex-acp's
	// model_reasoning_effort/model_reasoning_summary config keys (chat.go).
	thinking agent.ThinkingLevel
}

// NewCodex creates a new Codex backend with default settings. Its InitLaunch
// call necessarily diverges from NewClaudeCode/NewKiro's: codex
// alone needs Setup-time state (resolvedProjectDir/resolvedTrustAbsPath, see
// buildSurfaces) threaded into its CellDelivery.Build, so it supplies a bound
// method instead of the shared agent.BuildWellKnown every other well-known-
// file backend uses — a deliberate, reviewed divergence, not a missed sibling
// update.
// reprise:accept-drift
func NewCodex() *Codex {
	b := &Codex{}
	b.BaseBackend = agent.NewBaseBackend("codex", "1.0.0")
	b.BinaryPath = "codex"
	// codex routes delivery through the cell seam. RawContext: its context is the
	// content-addressed cache file a SessionStart hook reads at run time, so Setup
	// materializes that file (+ CTXLOOM_CONTEXT_FILE) as a pre-step. ContextHook:
	// codex is the one engine that fires the SessionStart inject-context hook, so
	// the hook is keyed to the cache file's hash. The config surface writes the
	// [hooks] (incl. that hook) + [mcp_servers] tables of .codex/config.toml.
	// Build is b.buildSurfaces (a method, not the shared BuildWellKnown every
	// other well-known-file backend uses) because codex — uniquely among them —
	// needs Setup-time state (resolvedProjectDir/resolvedTrustAbsPath) threaded
	// into its surfaces; see buildSurfaces' doc.
	b.InitLaunch(
		agent.NewBaseLifecycle("codex"),
		agent.NewBaseContextProvider(),
		nil, // SessionHistory: codex's rollout-*.jsonl scraper deleted — canonical capture is the only transcript source now
		&agent.CellDelivery{Build: b.buildSurfaces, RawContext: true, ContextHook: true},
	)
	b.SetExecuteEnv(b.cellCodexHomeEnv)
	b.SetACPTransport(CodexACPTransport) // intrinsic: every construction path (incl. direct) gets it
	return b
}

// resolveCodexProjectDir resolves the "virtual project dir" this run's codex
// surfaces target — cellScopedCodexHome(dir) is the FINAL CODEX_HOME
// (config.toml/prompts/skills/state/auth.json all hang off it). This is the
// single-owner fix for the env-precedence bug the per-engine-isolation-home
// plan found live: launch_backend.go's ExecuteEnv applies req.Env FIRST, then
// this backend's SetExecuteEnv contributor LAST, so cellCodexHomeEnv used to
// unconditionally override an isolation-provided CODEX_HOME
// (internal/lm/isolation/worktree.go's Env(), gated through
// credentialSeedSpecs["codex"]) with the in-tree <WorkDir>/.codex — the
// worktree config-home CODEX_HOME was DEAD.
//
// When the isolation layer already set CODEX_HOME, it is now the single
// owner: isolation/auth.go's codex HomeVar uses Subdir ".codex", so the value
// already ends in "/.codex" — this strips that suffix back to the virtual
// project dir cellScopedCodexHome expects, so every existing cell-scoped
// writer (cellScopedCodexHome/cellScopedPromptsDir/cellScopedSkillsDir,
// settings.go's SettingsPath) keeps resolving the join itself, unchanged; it
// simply joins against the isolation-provided config-home instead of the real
// WorkDir. isolated=true then, so the caller (Setup) knows this run's config
// home is ephemeral and safe to pre-seed trust into (see
// WriteSettingsWithTrust's doc) — an out-of-tree home that spills no engine
// state into the project tree and is removed at Worktree.Cleanup.
//
// THE PER-SESSION INSTANCE ARRIVES THROUGH THE SAME DOOR. Since S5 the in-tree
// controlled home is a PER-SESSION instance (SessionHome), and the decision to
// use one is `config_home: project` on the run's agent binding — read in
// exactly ONE place for all three engines, operations.InTreeAgentHomeEnv, which
// contributes CODEX_HOME=<instance>/.codex to the run env (and prepares its
// credentials) before this backend is ever constructed. So the instance reaches
// codex as an isolation-provided CODEX_HOME, above; codex does not resolve it
// itself and has no second opinion about whether one is warranted.
//
// No isolation-provided value falls back to two further cases:
//
//   - a ProcessIsolatedCell (container — req.CellKind) already runs inside a
//     fresh, per-container $HOME (isolation/runtime.go passes `-e HOME=...`
//     into every container spawn) that the isolation layer already bind-
//     mounted codex's auth.json into (codexCredentialMounts, auth.go) — so
//     $HOME/.codex IS the correct, already-authenticated CODEX_HOME
//     (this used to fall through to the WorkDir case
//     below, landing on the bind-mounted PROJECT dir instead — empty, no
//     auth.json, hence the 401 mismatch).
//   - anything else (None/shared-cwd, or no backend context) resolves to the
//     user's REAL ~/.codex — codexHomeRealHost. This is the D2 ruling
//     (2026-08-11): codex reads config_home like claude and kiro, and no
//     binding / an undeclared binding / an explicit `host` all keep the real
//     home. codex used to relocate CODEX_HOME here unconditionally, which was
//     defensible only while the relocation target was DURABLE; a disposable
//     per-session instance would have taken the human's own interactive codex
//     state — token refreshes, accumulated trust, sessions — away every single
//     session. Nothing is seeded into the real home and no trust is pre-seeded
//     into it: it is the user's, and ctxloom does not write a home it did not
//     create (see Setup and ensureCodexCredentials).
func resolveCodexProjectDir(env map[string]string, workDir string, cellKind agent.CellKind) (dir string, source codexHomeSource) {
	if home := env[CodexHomeEnv]; home != "" {
		if stripped := strings.TrimSuffix(home, string(filepath.Separator)+ConfigDirName); stripped != home {
			return stripped, codexHomeIsolationProvided
		}
		// A CODEX_HOME not in the expected "/.codex" shape — a future spec whose
		// Subdir changes, or (reachably) a user's own `env: {CODEX_HOME: ...}`,
		// which rides llmEnvFor into this run's env. Use it AS the project dir
		// directly: cellScopedCodexHome nests an extra ".codex" under it, which
		// is at least self-consistent (Setup and Execute still agree) even
		// though it is not the path that was asked for. Say so — the child gets
		// a home the caller never named, and silently is the one way that must
		// not happen.
		clidiag.Warn("ctxloom", "%s=%q does not end in %q; codex will read %s instead — name the %s directory itself to be delivered into it",
			CodexHomeEnv, home, string(filepath.Separator)+ConfigDirName, cellScopedCodexHome(home), ConfigDirName)
		return home, codexHomeIsolationProvided
	}
	if cellKind == agent.CellKindProcessIsolated {
		if home := os.Getenv("HOME"); home != "" {
			return home, codexHomeContainerFresh
		}
		// No $HOME even inside a container cell — every container spec sets
		// one (runtime.go), so this is unexpected; fall through to the
		// in-tree case below so this run still gets ACTIVE seeding
		// (ensureCodexCredentials) rather than silently landing on an
		// unverified home.
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// No resolvable home directory at all. Contribute NOTHING rather than
		// invent a relocation: an empty dir means cellCodexHomeEnv sets no
		// CODEX_HOME, so codex applies its own ~/.codex precedence and fails
		// on its own terms instead of ctxloom silently pointing it somewhere
		// nobody asked for.
		clidiag.Warn("ctxloom", "cannot resolve this machine's home directory (%v); leaving %s unset so codex uses its own default", err, CodexHomeEnv)
		return "", codexHomeRealHost
	}
	return home, codexHomeRealHost
}

// codexHomeSource classifies WHY resolveCodexProjectDir landed on the
// returned dir, so Setup knows which axis-specific credential handling
// applies (ensureCodexCredentials) — the three cases need three different
// treatments, not one:
type codexHomeSource int

const (
	// codexHomeRealHost: plain host run with no controlled home — no
	// isolation-provided CODEX_HOME, not a container cell. The dir is the
	// user's home directory, so cellScopedCodexHome lands on the REAL
	// ~/.codex. ctxloom did not create this home and must not prepare it:
	// no credential copy (the user's own `codex login` is what put auth.json
	// there) and NO trust pre-seed (codex's own accumulated
	// `[projects."..."]` entries are the user's answers, not ctxloom's to
	// write). Setup only VERIFIES there is something to authenticate with.
	codexHomeRealHost codexHomeSource = iota
	// codexHomeIsolationProvided: env["CODEX_HOME"] was contributed by a
	// ctxloom layer that also PREPARED it — the isolation package's worktree
	// Env() (gated through credentialSeedSpecs["codex"]) or, since S5, the
	// per-session instance operations.InTreeAgentHomeEnv resolves for a
	// binding declaring `config_home: project` (backends' codex
	// inTreeAgentHome spec, whose Prepare copies the host's auth.json in).
	// Either way the credentials landed before this run started and the
	// contributor already failed loud if they could not. Setup does nothing
	// further about credentials — but it DOES pre-seed trust, because this is
	// a home ctxloom created and codex has never seen.
	codexHomeIsolationProvided
	// codexHomeContainerFresh: a ProcessIsolatedCell's own $HOME — codex's
	// auth.json is already bind-mounted there by codexCredentialMounts
	// BEFORE this process started. Setup only VERIFIES the mount landed
	// (never copies: the destination IS the read-only mount target).
	codexHomeContainerFresh
)

// buildSurfaces is codex's CellDelivery.Build: unlike every other well-known-
// file backend (which use the shared agent.BuildWellKnown, ignoring
// isolatedDir entirely), codex needs its OWN Setup-time resolution
// (resolvedProjectDir/resolvedTrustAbsPath, computed by Setup below) threaded
// into NewSurfaces so its config/commands/skills surfaces target the SAME
// isolation-provided CODEX_HOME cellCodexHomeEnv points the launched child
// at — the single-owner fix. isolatedDir (the generic
// per-backend placement channel) is intentionally unused here: codex's
// resolution is backend-specific (env-derived, not cell-kind-derived).
func (b *Codex) buildSurfaces(in agent.SurfaceInputs, _ string) agent.SurfaceSet {
	return NewSurfaces(in, b.resolvedProjectDir, b.resolvedTrustAbsPath, nil)
}

// Setup resolves this run's CODEX_HOME ownership ONCE (resolveCodexProjectDir)
// before delegating to the shared LaunchBackend.Setup, which calls
// buildSurfaces — storing the result on b so cellCodexHomeEnv (Execute, later
// in the same request lifecycle: grpc/server.go's Run calls Setup then
// Execute on the same backend instance) reads the IDENTICAL value rather than
// re-deriving it from a possibly-different view of req. This is what makes
// Setup's delivery and Execute's env agree by construction, not convention.
func (b *Codex) Setup(ctx context.Context, req *agent.SetupRequest) error {
	dir, source := resolveCodexProjectDir(req.Env, req.WorkDir, req.CellKind)
	b.resolvedProjectDir = dir
	// Trust is pre-seeded for the WORKING DIRECTORY on every axis that points
	// codex at a home CTXLOOM ITSELF PROVISIONED and codex has therefore never
	// seen — the isolation-provided worktree home, the per-session instance, a
	// container cell's fresh $HOME. Without the pre-seed codex re-prompts for
	// workspace trust there, or (under `codex exec`, which has nobody to ask)
	// runs silently untrusted. See WriteSettingsWithTrust and
	// docs/trust-model.md.
	//
	// codexHomeRealHost is EXCLUDED, and that exclusion is the boundary the
	// trust model states: the real ~/.codex is the user's own, carrying codex's
	// own accumulated `[projects."<abs WorkDir>"]` answers from real prior
	// runs. ctxloom answers a trust prompt only for a home it created.
	//
	// The path is abs(WorkDir), NEVER the home directory: codex keys trust by
	// the cwd it runs in, so an entry naming the config home would answer a
	// question codex never asks.
	if source == codexHomeRealHost {
		b.resolvedTrustAbsPath = ""
	} else if abs, err := filepath.Abs(req.WorkDir); err == nil {
		b.resolvedTrustAbsPath = abs
	} else {
		b.resolvedTrustAbsPath = req.WorkDir
	}
	// FAIL-LOUD credential check: resolved and
	// stashed here (Setup errors are fault-tolerantly warned-and-ignored by
	// the grpc server — see its doc — so a credential failure recorded here
	// alone would never surface); Execute checks b.credentialErr FIRST and
	// refuses to launch codex at all when set, instead of the historical
	// silent 401/exit-0.
	b.credentialErr = ensureCodexCredentials(dir, source, resolveOpenAIAPIKey(req.Env, b.Env))
	return b.LaunchBackend.Setup(ctx, req)
}

// openAIAPIKeyEnv is codex's envTrigger — the variable that authenticates codex
// without any auth.json at all (internal/lm/isolation/auth.go's
// credentialSeedSpecs["codex"] names the same one).
const openAIAPIKeyEnv = "OPENAI_API_KEY"

// resolveOpenAIAPIKey answers what the CHILD will see for OPENAI_API_KEY, in
// BaseBackend.BuildEnv's own precedence: the run env (SetupRequest/
// ExecuteRequest.Env) wins over the backend's configured env (CodexConfig.Env),
// which wins over the ambient process env — the later assignment wins there, so
// the first map that DECLARES the key owns the value, including when it
// declares it empty. Reading only the ambient env would make a per-agent `env:`
// key invisible to the credential gate and refuse a run that authenticates.
func resolveOpenAIAPIKey(reqEnv, backendEnv map[string]string) string {
	if v, ok := reqEnv[openAIAPIKeyEnv]; ok {
		return v
	}
	if v, ok := backendEnv[openAIAPIKeyEnv]; ok {
		return v
	}
	return os.Getenv(openAIAPIKeyEnv)
}

// ensureCodexCredentials makes sure the CODEX_HOME this run is about to use
// (cellScopedCodexHome(dir)) actually has usable credentials. It NEVER COPIES —
// every axis's home is prepared by whoever created it, and the one home ctxloom
// did not create must not be written at all:
//
//   - codexHomeIsolationProvided: already seeded before this run started, by
//     internal/lm/isolation/worktree.go's provisionConfigHome (worktree
//     fan-out) or by the per-session instance's Prepare
//     (backends' codex inTreeAgentHome spec, via operations.
//     InTreeAgentHomeEnv). Both fail loud at their own choke gate when there
//     was nothing to copy, so this is a deliberate no-op.
//   - codexHomeContainerFresh: the container's fresh $HOME already has
//     auth.json bind-mounted in by codexCredentialMounts (isolation/auth.go)
//     before this process even started — copying here would read AND write the
//     SAME read-only-mounted file, so this only VERIFIES the mount landed.
//   - codexHomeRealHost: the user's own ~/.codex, which ctxloom never writes.
//     VERIFY ONLY — a missing auth.json here means the user has not run `codex
//     login`, and the fix is theirs, not a copy from somewhere else.
//
// No usable credential (and no OPENAI_API_KEY override) returns a clear,
// actionable error naming the fix. apiKey is the value the CHILD will see for
// OPENAI_API_KEY (resolveOpenAIAPIKey), which is not necessarily this process's
// own: a per-agent `env:` key authenticates the child just as well as an
// ambient one.
func ensureCodexCredentials(dir string, source codexHomeSource, apiKey string) error {
	if source == codexHomeIsolationProvided {
		return nil
	}
	if apiKey != "" {
		return nil // codex's envTrigger — auth rides the env, nothing to verify
	}
	authPath := filepath.Join(cellScopedCodexHome(dir), AuthFileName)
	if codexFileExists(authPath) {
		return nil
	}
	if source == codexHomeContainerFresh {
		return fmt.Errorf("codex: no OPENAI_API_KEY and no credentials at %s — this container's codex auth mount did not land; authenticate with `codex login` on the host (or set OPENAI_API_KEY), or check the container runtime/image", authPath)
	}
	return fmt.Errorf("codex: no OPENAI_API_KEY and no credentials at %s — run `codex login` (or set OPENAI_API_KEY); ctxloom does not write this home, it is your own", authPath)
}

// codexFileExists reports whether path is an existing regular file.
func codexFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// cellCodexHomeEnv is codex's per-backend child-env contributor. It reads the
// SAME resolution Setup already computed (b.resolvedProjectDir) so Execute's
// CODEX_HOME can never disagree with where Setup delivered config.toml/
// prompts/skills — the single-owner fix (see resolveCodexProjectDir). Falls
// back to re-deriving fresh from req only for a caller that reaches Execute
// without Setup having run first (defensive; the normal request lifecycle
// always runs Setup first — see Setup's doc — except SkipSetup, handled
// below). Skipped for a minimal/distill run (SkipSetup), which delivers no
// surfaces and should keep codex's global home.
//
// A ProcessIsolatedCell (container) is handled the SAME way as any other
// axis: resolveCodexProjectDir resolves it to the container's own fresh
// $HOME, which is a real, in-namespace, already-authenticated
// path (see resolveCodexProjectDir's doc and codexHomeContainerFresh) — not
// redundant and not non-existent, so no container-specific branch is needed
// here; the OPEN QUESTION this comment used to raise is resolved.
func (b *Codex) cellCodexHomeEnv(req *agent.ExecuteRequest) map[string]string {
	if req.SkipSetup {
		return nil
	}
	dir := b.resolvedProjectDir
	if dir == "" {
		dir, _ = resolveCodexProjectDir(req.Env, req.WorkDir, req.CellKind)
	}
	if dir == "" {
		// No home to name — resolveCodexProjectDir could not find this
		// machine's home directory and already warned. Contribute nothing so
		// codex applies its own ~/.codex precedence.
		return nil
	}
	return map[string]string{CodexHomeEnv: cellScopedCodexHome(dir)}
}

// Configure applies a decoded codex config to this backend.
func (b *Codex) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*CodexConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
		// Advisory validation only, matching claude-code's Configure and
		// agents.SetAgentRequest's tolerance for a typo'd cost-tuning knob:
		// an unrecognized (non-empty) value still resolves to the documented
		// medium default rather than failing the whole config.
		level, ok := agent.ParseThinkingLevel(c.Thinking)
		if c.Thinking != "" && !ok {
			clidiag.Warn("ctxloom", "codex config declares unknown thinking level %q (known: %s); using the default %q",
				c.Thinking, strings.Join(agent.ThinkingLevelNames(), "|"), agent.ThinkingMedium)
		}
		b.thinking = level
	}
}

// Execute runs the backend with the given request.
func (b *Codex) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// FAIL LOUD: Setup already resolved this run's
	// CODEX_HOME and checked it for usable credentials (ensureCodexCredentials).
	// A non-nil credentialErr means codex would otherwise launch straight into
	// a silent 401/exit-0 — refuse to spawn it at all and return the error
	// instead, so the failure is a loud, non-zero exit naming the actual fix.
	if b.credentialErr != nil {
		return nil, b.credentialErr
	}

	// Report ONLY the model we actually asked for. An empty req.Model means codex
	// resolves its own configured default, which lives server-side (its model list
	// is account-scoped and fetched) — so ctxloom cannot name it, and inventing a
	// fallback id here would stamp a model that never ran onto every consumer of
	// ModelInfo (bundle distillation records it as source provenance).
	modelInfo := &agent.ModelInfo{ModelName: req.Model, Provider: "openai"}

	// Codex's credential seeding is now handled entirely by Setup
	// (ensureCodexCredentials), which covers ALL THREE axes CODEX_HOME can
	// resolve to (see resolveCodexProjectDir / codexHomeSource) — not just the
	// worktree fan-out's isolation-provided home this comment used to name as
	// the sole mechanism:
	//   - codexHomeIsolationProvided (worktree fan-out, and the per-session
	//     instance a `config_home: project` binding gets): COPIED host-side
	//     BEFORE this run even starts — by isolation/worktree.go's
	//     provisionConfigHome or by the codex inTreeAgentHome spec's Prepare
	//     (both credentialSeedSpecs["codex"], auth.go).
	//   - codexHomeRealHost (plain host run): NOT copied and never written —
	//     it is the user's own ~/.codex, VERIFIED by Setup only.
	//   - codexHomeContainerFresh (container): VERIFIED by Setup against the
	//     bind-mounted auth.json in the container's own fresh $HOME
	//     — container alone does NOT isolate/seed CODEX_HOME by
	//     itself; it only provides the fresh $HOME namespace the mount lands
	//     in, and CODEX_HOME must actually be pointed there (see
	//     resolveCodexProjectDir) for the mount to be reachable at all.
	// By the time Execute runs (past the credentialErr check above), auth.json
	// is already in place for whichever axis applies (or OPENAI_API_KEY rides
	// the env instead) — nothing left to do here.

	// Context reaches Codex through the SessionStart hook + context file (the
	// shared file+hook mechanism), so Execute only forwards the context-file
	// path in the env (ExecuteCLI) — it never prepends context to the prompt.
	//
	// CONCURRENCY LIMIT (delegated agent_run fan-out): the SessionStart hook is registered
	// in a WORKSPACE-FIXED file (.codex/config.toml) with the per-run context hash
	// baked into its command, and Codex natively reads a WORKSPACE-FIXED AGENTS.md
	// — neither has a per-invocation redirect (Codex has no --mcp-config/--settings/
	// --append-system-prompt equivalent). So N codex agents in one cwd would each
	// rewrite config.toml — last writer wins → cross-agent context clobber. Unlike
	// claude (per-invocation flags) and kiro (per-agent agent-JSON `--agent`), codex
	// has NO redirection lever, so per-agent CONCURRENT isolation requires a
	// per-agent cwd (git worktree) or container. See the
	// per-agent-config-delivery memory note (ISOLATION AXIS).
	return b.ExecuteCLI(ctx, req, b.buildArgs(req), nil, modelInfo, stdout, stderr)
}

// buildArgs constructs the command-line arguments for the codex CLI. Oneshot runs
// go through the non-interactive `codex exec` subcommand; interactive runs launch
// the TUI directly.
//
// THE TWO SUBCOMMANDS DO NOT SHARE A FLAG SET, and an unknown flag is a hard
// exit-2 "unexpected argument", never a warning — so a posture flag must be
// checked against the subcommand that will receive it (codex-cli 0.144.4, `codex
// --help` / `codex exec --help`):
//
//	--sandbox <read-only|workspace-write|danger-full-access>  BOTH
//	--dangerously-bypass-approvals-and-sandbox                BOTH
//	--ask-for-approval <policy>                               INTERACTIVE ONLY
//	                     (`codex exec` rejects it — a non-interactive run has
//	                      nobody to ask, so it carries no approval policy)
//	--full-auto                                               NEITHER (removed;
//	                     codex deprecated it in favour of --sandbox)
//
// Every posture NAMES its sandbox rather than inheriting codex's default, so a
// change to that default upstream cannot silently relax ctxloom's stated posture:
//
//	plan / SkipSetup (distill, compaction) → read-only        (+ never approve)
//	default / acceptEdits                  → workspace-write  (codex has no
//	                                         edit-only tier)
//	bypass                                 → no sandbox, no approvals
//
// bypass deliberately does NOT reuse --sandbox workspace-write (codex's suggested
// --full-auto replacement): that is exactly the default posture's flag, which
// would collapse two distinct postures into one. The honest mapping of "bypass all
// permission checks" is codex's own full-access escape hatch, the peer of claude's
// --dangerously-skip-permissions.
func (b *Codex) buildArgs(req *agent.ExecuteRequest) []string {
	var args []string

	interactive := req.Mode != agent.ModeOneshot
	if !interactive {
		args = append(args, subcommandExec)
	}
	args = append(args, b.Args...)

	// Select the requested model (codex supports --model/-m). Empty lets codex use
	// its configured default rather than forcing one.
	if req.Model != "" {
		args = append(args, flagModel, req.Model)
	}

	sandbox := "workspace-write"
	neverApprove := false
	switch {
	case req.SkipSetup, req.Permissions == agent.PermissionPlan:
		// Minimal/distill and plan mean the same thing to codex: read the workspace,
		// change nothing, never stop to ask.
		sandbox, neverApprove = "read-only", true
	case req.Permissions == agent.PermissionBypass:
		sandbox = ""
		args = append(args, flagBypassApprovalsAndSandbox)
	}
	if sandbox != "" {
		args = append(args, flagSandbox, sandbox)
	}
	if neverApprove && interactive {
		args = append(args, flagAskForApproval, "never")
	}

	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, prompt)
	}

	return args
}
