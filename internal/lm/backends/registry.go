package backends

import (
	"os/exec"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/opencode"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
// registers only backend+config.
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
	// no surfaces (acp/mock); BuildSurfaces then returns an EmptySurfaceSet.
	newSurfaces func(agent.SurfaceInputs, afero.Fs) agent.SurfaceSet
	// exports maps loaded bundle content to this backend's command exports,
	// resolving its per-prompt enablement + metadata. nil = no command export.
	// Read by commandExportsFor / CommandExportsFor, which feed the commands surface
	// (SurfaceInputs.Commands) the enabled exports for the delivery seam.
	exports func([]*bundles.LoadedContent) []agent.CommandExport
	// skillExports maps loaded bundle skills to this backend's Agent Skill
	// package exports, resolving its per-skill enablement. nil = no skill
	// export (every backend but claude today — Part B3-seam; the parallel
	// codex/opencode/kiro/agy wave populates this next). Read by
	// skillExportsFor / SkillExportsFor, the skills-surface analog of
	// commandExportsFor / CommandExportsFor.
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
}

// descriptors holds the per-agent descriptor table, keyed by backend name.
var descriptors = make(map[string]*agentDescriptor)

// registerDescriptor installs a backend's complete descriptor.
func registerDescriptor(d agentDescriptor) {
	descriptors[d.name] = &d
}

// descriptorFor returns the named descriptor, creating an empty one if absent.
// It backs the incremental Register/RegisterConfig entry points, which remain
// for callers (and tests) that register a backend piecemeal.
func descriptorFor(name string) *agentDescriptor {
	d, ok := descriptors[name]
	if !ok {
		d = &agentDescriptor{name: name}
		descriptors[name] = d
	}
	return d
}

// Register adds a backend constructor to the registry.
func Register(name string, constructor func() agent.Backend) {
	descriptorFor(name).newBackend = constructor
}

// Get returns a new instance of the named backend.
func Get(name string) agent.Backend {
	if d, ok := descriptors[name]; ok && d.newBackend != nil {
		return d.newBackend()
	}
	return nil
}

// List returns all registered backend names.
func List() []string {
	names := make([]string, 0, len(descriptors))
	for name, d := range descriptors {
		if d.newBackend != nil {
			names = append(names, name)
		}
	}
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
// agent.BaseBackend satisfies it (see agent.BaseBackend.GetBinaryPath), so every
// backend embedding it is a provider.
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

// IsAvailable returns true if the backend's default binary is installed and in PATH.
func IsAvailable(name string) bool {
	binary := GetDefaultBinary(name)
	if binary == "" {
		return false
	}
	_, err := exec.LookPath(binary)
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
	// claudeACPTransport: claude-code has no native ACP mode, so structured
	// chat rides Zed's third-party claude-code-acp adapter (internal/claude/
	// chat.go). Binary/InstallCmd are the exact literals that chat.go's
	// Chat() gate and error text used before this generalization (claude.
	// ClaudeACPAdapter, "npm install -g @zed-industries/claude-code-acp").
	// Publisher/SourceRepo are derived from the adapter's own npm scope
	// (@zed-industries/...) and confirmed live: github.com/zed-industries/
	// claude-code-acp.
	claudeACPTransport = agent.ACPTransport{
		Kind:       agent.ACPAdapter,
		Binary:     claude.ClaudeACPAdapter,
		InstallCmd: "npm install -g @zed-industries/claude-code-acp",
		Publisher:  "Zed Industries",
		SourceRepo: "https://github.com/zed-industries/claude-code-acp",
	}
	// codexACPTransport: codex has no native ACP mode either, wrapped by
	// Zed's codex-acp adapter (internal/codex/chat.go). Same npm scope as
	// claude's (@zed-industries/codex-acp — verified live: github.com/
	// zed-industries/codex-acp), so Publisher/SourceRepo mirror claude's
	// shape rather than being left empty.
	codexACPTransport = agent.ACPTransport{
		Kind:       agent.ACPAdapter,
		Binary:     codex.CodexACPAdapter,
		InstallCmd: "npm install -g @zed-industries/codex-acp",
		Publisher:  "Zed Industries",
		SourceRepo: "https://github.com/zed-industries/codex-acp",
	}
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
			b := claude.NewClaudeCode()
			b.SetLauncher(RunLaunchSpec)
			b.SetACPTransport(claudeACPTransport)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &claude.ClaudeConfig{})
		},
		newWriter: claude.NewWriter,
		// claude's NewSurfaces adapts the shared inputs to its LOCAL SurfaceInputs
		// and binds an out-of-cwd placement for the race-safe variants; the
		// well-known Deliveries() path materialize drives never dereferences it, so
		// a wellKnownPlacement is fine.
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet {
			return claude.NewSurfaces(claude.SurfaceInputs{
				Context:               in.Context,
				MCP:                   in.MCP,
				BundleMCP:             in.BundleMCP,
				Hooks:                 in.Hooks,
				ManageStatusline:      in.ManageStatusline,
				Commands:              in.Commands,
				SelfContainedCommands: in.SelfContainedCommands,
				Skills:                in.Skills,
				SelfContainedSkills:   in.SelfContainedSkills,
			}, wellKnownPlacement{}, fs)
		},
		exports:              claudeExports,
		skillExports:         claudeSkillExports,
		enforcesReadOnlyPlan: true, // --permission-mode plan is read-only
		acpTransport:         claudeACPTransport,
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
			b := codex.NewCodex()
			b.SetLauncher(RunLaunchSpec)
			b.SetACPTransport(codexACPTransport)
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
		acpTransport:         codexACPTransport,
	})

	// Kiro (direct-CLI path via `kiro-cli chat`). Materializes native config the
	// agent reads from cwd: the ctxloom agent (.kiro/agents/ctxloom.json — hooks +
	// skill resources), MCP (.kiro/settings/mcp.json), context (.kiro/steering/),
	// commands AND Agent Skills, both under .kiro/skills/<n>/SKILL.md — the one
	// engine where those two surfaces collide (D6 skill-wins, see
	// kiro.filterClaimedCommands in kiro/surfaces.go).
	// LIVE-UNTESTED: never run against a logged-in kiro-cli on any dev host
	// (see the package doc in internal/kiro for what's proven vs unverified;
	// taskloom numb-panda / bold-smirk track the revive).
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
		// LIVE VERIFIED 2026-07-15 (authenticated kiro-cli 2.12.1, tangy-fox):
		// `--trust-tools=fs_read` genuinely denies a headless fs_write — a
		// sentinel-file overwrite left the file byte-unchanged and kiro-cli
		// printed "Command fs_write is rejected because it matches one or
		// more rules on the denied list". `--trust-tools=fs_read,fs_write`
		// and `--trust-all-tools` (positive controls) both let the same write
		// land. See kiro.buildArgs (backend.go) for the mapping.
		enforcesReadOnlyPlan: true,
		acpTransport:         kiroACPTransport,
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
	})

	// Mock registers only backend+config: no settings writer, no command
	// export (descriptor fields are optional).
	registerDescriptor(agentDescriptor{
		name:       "mock",
		newBackend: func() agent.Backend { return NewMock() },
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &MockConfig{})
		},
	})
}
