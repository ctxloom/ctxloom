package backends

import (
	"fmt"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/opencode"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// Configurable is implemented by backends that accept their own typed config.
// The argument is the backend's concrete BackendConfig (decoded by the config
// registry), so no shared code ever type-switches on backend specifics.
type Configurable interface {
	Configure(cfg agent.BackendConfig)
}

// agentDescriptor is the single per-agent registration record. Every
// cross-backend dispatch in this package (backend construction, config
// decoding, settings writing, slash-command export/writing) is a view over
// this table, so adding an agent means registering ONE descriptor here —
// not touching four separate maps/switches.
//
// Only name and newBackend are mandatory. The optional fields gate
// capability-specific dispatch: a nil newSurfaces means the backend materializes
// no surfaces (BuildSurfaces returns an EmptySurfaceSet); a nil newWriter means it
// has no settings-writer dispatch (BackendsWithSettings omits it, GetSettingsWriter
// returns nil); a nil exports means no slash-command export (CommandExportsFor
// yields nil, so the commands surface has nothing to write). The mock backend
// registers backend+config+surfaces+skillExports — no settings writer, no
// command export; its surfaces are context and skills (see mock_surfaces.go).
type agentDescriptor struct {
	// name is the backend's registry key and must match its module's Name().
	name string
	// newBackend constructs a fresh backend instance (wrapping the module
	// ctor + launcher injection for local-CLI agents).
	newBackend func() agent.Backend
	// decodeConfig turns a labeled LLM entry's raw body into the backend's
	// typed config (see DecodeLLMConfig).
	decodeConfig configDecoder
	// newWriter constructs the backend's settings writer from resolved
	// options. nil = backend has no settings support.
	newWriter func(agent.SettingsOptions) agent.SettingsWriter
	// newSurfaces builds the backend's SurfaceSet from a run's shared inputs and a
	// filesystem (nil = OS fs), so a name-only caller (materialize) can deliver
	// every native surface through a cell without importing the concrete backend.
	// It is the delivery-seam counterpart of newWriter. nil = backend materializes
	// no surfaces (acp, whose native config format is a launch-time detail of
	// the ACP driver, not this descriptor's business); BuildSurfaces then
	// returns an EmptySurfaceSet. mock is NOT in that set: it registers a
	// real newSurfaces (context + skills, mock_surfaces.go) so hermetic
	// delivery tests can prove a fragment or a skill package actually reached
	// a written file.
	newSurfaces func(agent.SurfaceInputs, afero.Fs) agent.SurfaceSet
	// exports maps loaded bundle content to this backend's command exports,
	// resolving its per-prompt enablement + metadata. nil = no command export.
	// Read by CommandExportsFor, which feed the commands surface
	// (SurfaceInputs.Commands) the enabled exports for the delivery seam.
	exports func([]*bundles.LoadedContent) []agent.CommandExport
	// skillExports maps loaded bundle skills to this backend's Agent Skill
	// package exports, resolving its per-skill enablement. nil = no skill
	// export (acp today). Read by SkillExportsFor, the skills-surface analog
	// of CommandExportsFor.
	skillExports func([]*bundles.LoadedSkill) []agent.SkillExport
	// enforcesReadOnlyPlan is true when the backend maps agent.PermissionPlan to a
	// genuinely read-only, non-prompting mode (see the backend's buildArgs plan
	// branch). false backends have no read-only tier, so plan would run
	// unrestrained — the run resolver collapses plan to default for them. Keep in
	// sync with the buildArgs plan mapping when a backend gains/loses the mode.
	enforcesReadOnlyPlan bool
	// acpTransport is this backend's single ACP-transport declaration (see
	// agent.ACPTransport's doc): native/adapter/bespoke, and — for an adapter
	// engine — the binary, install command, and provenance. Read by
	// ACPTransportFor (consumers with no backend instance, e.g. doctor_cmd.go)
	// and injected into the constructed instance via SetACPTransport in
	// newBackend (consumers that ARE the instance, e.g. claude/codex's Chat()
	// gate) — ONE value, two read paths, never a third hardcoded copy.
	acpTransport agent.ACPTransport
	// resolveModel translates a configured model string into the concrete id
	// this backend's launch path requires (claude's ACP nickname→concrete-id
	// table today — see claude.ResolveModel), returning ok=false when the
	// given model cannot be resolved to anything the launch path accepts. nil
	// = the backend's model passes through untouched (every backend but
	// claude-code today). Read by ResolveModelFor (delegate_seams.go), the
	// polymorphic replacement for operations' old claude-only branch
	// (ADR-0026).
	resolveModel func(model string) (resolved string, ok bool)
	// hookGlobalScopePaths resolves this backend's project-scoped config path
	// (under a workDir) and its bare user-GLOBAL path, for backends that carry
	// a project/global collision class `manage hooks install` must guard
	// against (see the claude-code descriptor below for the collision itself,
	// and CheckHookTargetScope in delegate_seams.go for how it's used). nil =
	// audited, no guard needed (antigravity has no usable global location;
	// opencode's global path never collapses onto its project path — see
	// operations.checkHookTargetScope's historical doc, preserved there).
	hookGlobalScopePaths func(workDir string) (projectPath, globalPath string, err error)
	// hookGlobalScopeLabel is the human-facing name for this backend's global
	// scope, read into CheckHookTargetScope's refusal/warning message (e.g.
	// "Claude Code's user-global settings file"). Empty when
	// hookGlobalScopePaths is nil.
	hookGlobalScopeLabel string
	// noHooksReason declares, in one clause, that this backend has NO hook
	// mechanism AT ALL and says why ("opencode has no hook mechanism"). Empty
	// means the backend carries hooks — every backend but opencode today.
	//
	// It is the whole-mechanism twin of agent.HookRoute.Unsupported (which says
	// the same thing about ONE unified event on a backend that does have hooks)
	// and exists for the same reason: a hook set written nowhere is
	// indistinguishable from a hook set nobody declared, so the absence has to
	// be DECLARED to be reportable. Read by UncarriedSurfaces.
	//
	// TestDeliveryApproach_HookCarriageMatchesDeclaration (tests/integration)
	// holds this field honest against the delivered payload, so it cannot drift
	// from what the backend's settings writer actually does.
	noHooksReason string
	// unsupportedHookKinds is the PER-EVENT twin of noHooksReason, for a
	// backend that has a hook mechanism generally but lacks a native event
	// for specific unified KINDS ("session_end") — keyed by the same kind
	// string a HookRoute.Kind declares at write time (e.g. codex's
	// addUnifiedHooks route for u.SessionEnd), valued with that SAME
	// Unsupported reason, so UncarriedSurfaces can report the identical loss
	// to a caller that never writes settings (doctor/agent show/acp list)
	// without hand-maintaining a second copy of either string. nil = every
	// kind this backend's mechanism carries is natively supported.
	unsupportedHookKinds map[string]string
}

// descriptors holds the per-agent descriptor table, keyed by backend name.
var descriptors = make(map[string]*agentDescriptor)

// registerDescriptor installs a backend's complete descriptor. Panics on a
// duplicate name: this only ever runs at init() time from the
// literal calls below, so a collision is a programming error (a new backend
// accidentally reusing an existing name) — silently letting the second
// registration win would drop the first's writer/surfaces/exports with no
// signal at all.
func registerDescriptor(d agentDescriptor) {
	if _, dup := descriptors[d.name]; dup {
		panic("backends: duplicate descriptor registration for " + d.name)
	}
	descriptors[d.name] = &d
}

// descriptorFor returns the named descriptor, creating an empty one if absent.
// It backs RegisterHookGlobalScopeForTesting (delegate_seams.go) — a test-only
// piecemeal registration seam. The piecemeal Register/RegisterConfig entry
// points it originally backed were deleted as dead (zero callers
// anywhere, repo-wide); built-in backends register their complete descriptor
// via registerDescriptor (below) in one call.
func descriptorFor(name string) *agentDescriptor {
	d, ok := descriptors[name]
	if !ok {
		d = &agentDescriptor{name: name}
		descriptors[name] = d
	}
	return d
}

// Get returns a new instance of the named backend.
func Get(name string) agent.Backend {
	if d, ok := descriptors[name]; ok && d.newBackend != nil {
		return d.newBackend()
	}
	return nil
}

// List returns all registered backend names, sorted — map
// iteration order is randomized per Go's spec, so every caller (shell
// completion, help output) previously had to sort defensively or accept
// nondeterministic output; several already did (llm_list.go, operations/llm.go,
// init.go), which this makes redundant.
func List() []string {
	names := make([]string, 0, len(descriptors))
	for name, d := range descriptors {
		if d.newBackend != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Exists returns true if a backend with the given name is registered.
func Exists(name string) bool {
	d, ok := descriptors[name]
	return ok && d.newBackend != nil
}

// EnforcesReadOnlyPlan reports whether the named backend maps
// agent.PermissionPlan to a genuinely read-only, non-prompting mode (claude
// --permission-mode plan, codex --sandbox read-only, opencode.json permission
// {edit:deny, bash:deny}, kiro --trust-tools=fs_read). Backends that don't
// (antigravity, acp) would run plan unrestrained and can't be trusted to be
// headless-safe for it, so the run resolver collapses plan to default for
// them. antigravity is the deliberate exception even though it now PASSES
// --mode plan (backend.go's buildArgs): that flag was LIVE VERIFIED
// (2026-07-15, authenticated agy 1.1.2) to NOT enforce read-only in headless
// `-p` execution — self-reported "not in read-only mode", a sentinel write
// and a probe shell command both succeeded unblocked. Flipping this true
// would tell the resolver to trust a flag proven not to work, the exact
// silent-no-op class this codebase treats as a bug, not a shortcut. Revisit
// if a future agy release actually enforces it. An unregistered name reports
// false.
func EnforcesReadOnlyPlan(name string) bool {
	d, ok := descriptors[name]
	return ok && d.enforcesReadOnlyPlan
}

// ACPTransportFor returns the named backend's declared ACP transport (see
// agent.ACPTransport) — the single source every consumer without a live
// backend instance reads (doctor's DOCTOR-CHECK-ACPADAPTER-m3, init PRIME's
// mirror of it, container-image install-fragment generation), instead of a
// second hardcoded claude/codex name switch. An unregistered name reports the
// zero value (agent.ACPNative, everything else empty) — "needs nothing" is
// the safe default for a name this registry doesn't know.
func ACPTransportFor(name string) agent.ACPTransport {
	d, ok := descriptors[name]
	if !ok {
		return agent.ACPTransport{}
	}
	return d.acpTransport
}

// BinaryPathProvider is implemented by backends that expose their binary path.
// agent.BaseBackend satisfies it (see agent.BaseBackend.GetBinaryPath), so
// every backend registered in THIS package's descriptor table is a provider.
// The type assertion below might look like it buys nothing on that
// evidence alone, but it is not dead: agent.Backend itself does not require
// GetBinaryPath (internal/lm/grpc/server_test.go's fakeBackend implements
// agent.Backend without it), so widening the interface directly would break
// that non-BaseBackend-embedding implementor. The optional-capability
// assertion here is the correct shape for a genuinely optional capability,
// not dead defensiveness.
type BinaryPathProvider interface {
	GetBinaryPath() string
}

// GetDefaultBinary returns the default binary name for a backend by instantiating it.
func GetDefaultBinary(name string) string {
	backend := Get(name)
	if backend == nil {
		return ""
	}
	if provider, ok := backend.(BinaryPathProvider); ok {
		return provider.GetBinaryPath()
	}
	return ""
}

// AvailabilityOf resolves the named backend's default binary and reports
// where it was found on PATH (or the login-shell PATH fallback), or the
// reason it could not be — IsAvailable's plain bool used to collapse
// "unregistered backend", "backend has no default binary", and "binary not
// resolvable on PATH" into the same false, leaving a caller like
// `ctxloom init` (which uses IsAvailable to decide which engines to offer)
// with no way to explain why one is missing.
func AvailabilityOf(name string) (string, error) {
	binary := GetDefaultBinary(name)
	if binary == "" {
		return "", fmt.Errorf("backend %q has no default binary to resolve", name)
	}
	return shellenv.Resolve(binary)
}

// IsAvailable returns true if the backend's default binary is installed and
// resolvable — via the process's own inherited PATH, or (shellenv.Resolve's
// fallback) the user's login-shell PATH, so a GUI-launched ctxloom (minimal
// inherited PATH) reports the same availability a terminal-launched one
// would. A thin boolean convenience over AvailabilityOf; use that directly
// when the reason for unavailability matters.
func IsAvailable(name string) bool {
	_, err := AvailabilityOf(name)
	return err == nil
}

// Per-engine ACP-transport declarations (agent.ACPTransport) — the single
// source registerDescriptor's acpTransport field AND each constructed
// backend's SetACPTransport injection both read, so a claude/codex Chat()
// gate, DOCTOR-CHECK-ACPADAPTER-m3, and (isolation/profile.go, separately,
// since that package deliberately does not import this one — see its own
// doc) the Containerfile install fragment can never disagree about the
// binary name or install command for the same engine.
var (
	// The two ADAPTER engines declare their transport in their OWN packages
	// (claude.ClaudeACPTransport, codex.CodexACPTransport) so their
	// constructors set it on every instance — including direct construction
	// outside this registry — instead of relying on registry-only injection
	// (which left an un-injected instance defaulting to ACPNative and skipping
	// its adapter). This block declares only the engines whose transport has
	// no package-level home to live in: the native/bespoke cases below, whose
	// zero-ish values are correct by construction and whose Chat() either has
	// no adapter gate (native) or bypasses the acp package entirely (bespoke).
	//
	// kiroACPTransport: kiro-cli speaks ACP natively (`kiro-cli acp` —
	// internal/kiro/chat.go) — no separate adapter binary.
	kiroACPTransport = agent.ACPTransport{Kind: agent.ACPNative}
	// opencodeACPTransport: opencode speaks ACP natively (`opencode acp` —
	// internal/opencode/chat.go) — no separate adapter binary.
	opencodeACPTransport = agent.ACPTransport{Kind: agent.ACPNative}
	// antigravityACPTransport: agy has neither a native ACP mode nor a
	// first-party adapter; its StructuredChat is a bespoke prose driver over
	// `agy -p` (internal/antigravity/chat.go) — no adapter to install or
	// probe.
	antigravityACPTransport = agent.ACPTransport{Kind: agent.ACPBespoke}
	// acpGenericACPTransport: the generic "acp" backend drives WHATEVER
	// ACP-speaking command config supplies (`command: "kiro-cli acp"`,
	// `claude-code-acp`, ...) — from this backend's own point of view that
	// command is a native passthrough, not an adapter it manages; provenance
	// vetting for a THIRD-PARTY command configured here is the user's own
	// job, same posture as any other config value.
	acpGenericACPTransport = agent.ACPTransport{Kind: agent.ACPNative}
)

// Every backend registered here reaches its model by spawning the VENDOR'S OWN agent
// binary (claude, codex, kiro-cli, agy) or that vendor's ACP adapter. ctxloom holds no
// provider SDK and makes no direct call to any model API — and must not acquire one on
// any path that carries subscription credentials.
//
// This is a licensing invariant, not a style preference. Anthropic reserves subscription
// OAuth for "ordinary use of Claude Code and other native Anthropic applications", bars
// tools that "misrepresent their identity to Anthropic's servers" or "route third-party
// traffic against subscription limits", and directs anyone building on the Agent SDK to
// API keys instead (support article 13189465, updated 2026-05-19; code.claude.com
// legal-and-compliance). Lifting a subscription token into our own HTTP client is the
// prohibited act — it is precisely the identity misrepresentation named above. Launching
// the vendor's signed-in binary as a child process is not: the genuine binary makes the
// call, so there is nothing to misrepresent, and Anthropic names `claude -p` as drawing
// on subscription limits, i.e. metered rather than banned. Adding anthropic-sdk-go /
// openai-go / langchaingo "to simplify the launcher" would forfeit that standing.
//
// The compliance therefore lives in the SHAPE of this table, not in any one backend.
//
// Metered BYO-API-key access through a gateway (OpenRouter, LiteLLM) is fine — but a
// gateway serves Anthropic *models*, never Claude *Code*, and a subscription-authenticated
// CLI cannot be pointed at one.
func init() {
	// Register all built-in backends — ONE descriptor per agent covering
	// construction, config decoding, settings writing, and slash-command
	// export. Each local-CLI backend gets ctxloom's pty-backed launcher
	// injected — the substrate no longer execs processes itself.
	registerDescriptor(agentDescriptor{
		name: "claude-code",
		newBackend: func() agent.Backend {
			b := claude.NewClaudeCode() // sets its own ACPTransport intrinsically
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &claude.ClaudeConfig{})
		},
		newWriter: claude.NewWriter,
		// claude takes the shared agent.SurfaceInputs directly rather than a
		// local copy: two hand-maintained field-by-field mappers drift apart, as
		// they did on MCPCommandOverride. It binds an out-of-cwd
		// placement for the race-safe variants; this path never delivers one, so
		// a wellKnownPlacement is fine.
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet {
			return claude.NewSurfaces(in, wellKnownPlacement{}, fs)
		},
		exports:              claudeExports,
		skillExports:         claudeSkillExports,
		enforcesReadOnlyPlan: true, // --permission-mode plan is read-only
		acpTransport:         claude.ClaudeACPTransport,
		resolveModel:         claude.ResolveModel,
		// claude's project settings.json (claude.ProjectSettingsPath) collapses
		// onto its user-global one (claude.GlobalSettingsPath) exactly when
		// workDir == $HOME — found live 2026-07-14 (`manage
		// hooks install` run from $HOME silently went global).
		hookGlobalScopePaths: func(workDir string) (string, string, error) {
			global, err := claude.GlobalSettingsPath()
			return claude.ProjectSettingsPath(workDir), global, err
		},
		hookGlobalScopeLabel: "Claude Code's user-global settings file",
	})

	registerDescriptor(agentDescriptor{
		name: "antigravity",
		newBackend: func() agent.Backend {
			b := antigravity.NewAntigravity()
			b.SetLauncher(RunLaunchSpec)
			b.SetACPTransport(antigravityACPTransport)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &antigravity.AntigravityConfig{})
		},
		newWriter:    antigravity.NewWriter,
		newSurfaces:  func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return antigravity.NewSurfaces(in, fs) },
		exports:      antigravityExports,
		skillExports: antigravitySkillExports,
		acpTransport: antigravityACPTransport,
	})

	// LIVE-UNTESTED: codex has never been run against a real account on any
	// dev host (see the package doc in internal/codex for what's proven vs
	// unverified; taskloom bold-smirk tracks the revive).
	registerDescriptor(agentDescriptor{
		name: "codex",
		newBackend: func() agent.Backend {
			b := codex.NewCodex() // sets its own ACPTransport intrinsically
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &codex.CodexConfig{})
		},
		newWriter: codex.NewWriter,
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet {
			// The static apply/materialize path has no isolation context — no
			// homeOverride/trustAbsPath, exactly as before those params existed
			// (the live run/launch path wires them via Codex.buildSurfaces
			// instead, which does not go through this registry closure).
			return codex.NewSurfaces(in, "", "", fs)
		},
		exports:              codexExports,
		skillExports:         codexSkillExports,
		enforcesReadOnlyPlan: true, // plan → --sandbox read-only (both subcommands; see codex.buildArgs)
		acpTransport:         codex.CodexACPTransport,
		// codex has hooks generally (unlike opencode, so noHooksReason stays
		// empty) but no native session_end event — see codex.NoSessionEndReason
		// and addUnifiedHooks' route for the write-time half of this same fact.
		unsupportedHookKinds: map[string]string{
			bundles.HookEventSessionEnd: codex.NoSessionEndReason,
		},
		// codex's project config-home (codex.ProjectHome) collapses onto its
		// bare GLOBAL home (codex.GlobalHome) exactly when workDir == $HOME --
		// the same collision class found for claude.
		hookGlobalScopePaths: func(workDir string) (string, string, error) {
			global, err := codex.GlobalHome()
			return codex.ProjectHome(workDir), global, err
		},
		hookGlobalScopeLabel: "codex's global home",
	})

	// Kiro (direct-CLI path via `kiro-cli chat`). Materializes native config the
	// agent reads from cwd: the ctxloom agent (.kiro/agents/ctxloom.json — hooks +
	// skill resources), MCP (.kiro/settings/mcp.json), context (.kiro/steering/),
	// commands AND Agent Skills, both under .kiro/skills/<n>/SKILL.md — the one
	// engine where those two surfaces collide (D6 skill-wins, see
	// kiro.filterClaimedCommands in kiro/surfaces.go).
	// LIVE-VERIFIED against an authenticated kiro-cli — see the package doc in
	// internal/kiro for exactly what was proven (backend parity, a real oneshot
	// chat, and --model honor confirmed two independent ways).
	registerDescriptor(agentDescriptor{
		name: "kiro",
		newBackend: func() agent.Backend {
			b := kiro.NewKiro()
			b.SetLauncher(RunLaunchSpec)
			b.SetACPTransport(kiroACPTransport)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &kiro.KiroConfig{})
		},
		newWriter:    kiro.NewWriter,
		newSurfaces:  func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return kiro.NewSurfaces(in, fs) },
		exports:      kiroExports,
		skillExports: kiroSkillExports,
		// LIVE VERIFIED 2026-07-15 (authenticated kiro-cli 2.12.1):
		// `--trust-tools=fs_read` genuinely denies a headless fs_write — a
		// sentinel-file overwrite left the file byte-unchanged and kiro-cli
		// printed "Command fs_write is rejected because it matches one or
		// more rules on the denied list". `--trust-tools=fs_read,fs_write`
		// and `--trust-all-tools` (positive controls) both let the same write
		// land. See kiro.buildArgs (backend.go) for the mapping.
		enforcesReadOnlyPlan: true,
		acpTransport:         kiroACPTransport,
		// kiro's project .kiro dir (kiro.ProjectHome) collapses onto its bare
		// GLOBAL home (kiro.GlobalHome) exactly when workDir == $HOME --
		// the same collision class found for claude.
		hookGlobalScopePaths: func(workDir string) (string, string, error) {
			global, err := kiro.GlobalHome()
			return kiro.ProjectHome(workDir), global, err
		},
		hookGlobalScopeLabel: "kiro's global home",
	})

	// ACP (generic Agent Client Protocol client): drives ANY ACP-capable agent
	// chosen by config (`command: "kiro-cli acp"`, `claude-code-acp`, a future
	// `agy acp`) — new ACP agents become CONFIG, not code. Structured chat +
	// headless oneshot only (no TUI). It deliberately registers NO settings
	// writer and NO command exports: a GENERIC agent has no known native config
	// format to materialize (context still reaches a run as the lead fragment /
	// prompt). The KNOWN agents' ACP paths ride their OWN backends — kiro/codex
	// StructuredChat delegates to this driver — where materialization is the
	// target's own writer; that is the settings-delegation answer, so no
	// per-target "acp-<agent>" descriptors exist.
	registerDescriptor(agentDescriptor{
		name: "acp",
		newBackend: func() agent.Backend {
			b := acp.NewACP()
			b.SetACPTransport(acpGenericACPTransport)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &acp.ACPConfig{})
		},
		// A GENERIC ACP agent has no known native config format to materialize, so
		// it opts out with an empty surface set (mirrors its nil settings writer).
		newSurfaces:  func(agent.SurfaceInputs, afero.Fs) agent.SurfaceSet { return agent.EmptySurfaceSet{} },
		acpTransport: acpGenericACPTransport,
	})

	// opencode (first-party `opencode acp`, HOST-only chat spine). Slice 2 adds the
	// settings/materialization seam: ctxloom's managed keys are merged into a
	// project-local, strictly-validated opencode.json — MCP servers (`mcp`),
	// assembled context (`instructions` -> .opencode/ctxloom-context.md), and, on the
	// live chat path only, a GENUINE read-only `permission` for plan mode. Slice 3
	// adds command (commands) materialization: enabled bundle prompts become
	// opencode custom commands (.opencode/command/<name>.md), delivered by the
	// commands surface on the static `profile materialize` path and transiently
	// in Chat on the LIVE path (written before the run, reverted after — same
	// no-debris shape as the opencode.json overlay). The newSurfaces builder
	// serves materialize (mcp + context + commands).
	// enforcesReadOnlyPlan is TRUE: the written permission denies edit (which gates
	// opencode's write tool too) AND bash, so a plan run genuinely cannot mutate —
	// stricter than opencode's built-in `plan` agent, which leaves bash allowed.
	// Session-history and interactive PTY launch are later slices.
	registerDescriptor(agentDescriptor{
		name: "opencode",
		newBackend: func() agent.Backend {
			b := opencode.NewOpencode()
			b.SetLauncher(RunLaunchSpec)
			b.SetACPTransport(opencodeACPTransport)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &opencode.OpencodeConfig{})
		},
		newWriter:            opencode.NewWriter,
		newSurfaces:          func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return opencode.NewSurfaces(in, fs) },
		exports:              opencodeExports,
		skillExports:         opencodeSkillExports,
		enforcesReadOnlyPlan: true, // plan -> opencode.json permission {edit:deny, bash:deny}
		acpTransport:         opencodeACPTransport,
		// opencode is the one backend with no hooks surface of any shape:
		// opencode.json has no hook key, there is no settings event vocabulary
		// to route the six unified events onto, and OpencodeWriter.WriteSettings
		// accepts a *wire.HooksConfig it cannot do anything with. Declared here
		// so `profile materialize` can SAY so instead of writing four true
		// "wrote" lines over a silently dropped guardrail (whiny-exclusive).
		noHooksReason: "opencode has no hook mechanism",
	})

	// Mock registers backend+config+surfaces+skillExports: still no settings
	// writer and no command export (descriptor fields are optional) — but it
	// DOES build a real SurfaceSet, so BuildSurfaces("mock", …) materializes a
	// hermetic MOCK_CONTEXT.md and a hermetic .mock/skills/ tree instead of
	// returning agent.EmptySurfaceSet (see mock_surfaces.go).
	registerDescriptor(agentDescriptor{
		name:       "mock",
		newBackend: func() agent.Backend { return NewMock() },
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &MockConfig{})
		},
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return NewMockSurfaces(in, fs) },
		// Without this mapper SurfaceInputs.Skills is always empty for mock and
		// the skills surface above delivers nothing — a surface that exists,
		// reports success and writes zero bytes, which is precisely the
		// silent no-op the mock engine exists to catch in others.
		skillExports: mockSkillExports,
		// mock is the SAME structural shape as opencode here: mockApproaches
		// (mock_surfaces.go) declares context and skills, so a configured
		// session_start hook has no settings surface to land on and reaches no
		// file. Declared for the same reason opencode's noHooksReason is —
		// UncarriedSurfaces can only report a loss that is DECLARED, and an
		// undeclared one reads as silence (whiny-exclusive). When mock gains a
		// settings/hook surface (tracked separately), delete this line; the
		// hook sentinel will then need a real destination instead.
		noHooksReason: "mock has no settings/hook surface",
	})
}
