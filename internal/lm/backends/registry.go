package backends

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	// parked_engines: codex/kiro/opencode are out of the default build (see
	// internal/codex's package doc) so their imports and registrations below
	// are commented out, not deleted. grep -rn parked_engines finds every site.
	// "github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/engineversion"
	// "github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	// "github.com/ctxloom/ctxloom/internal/opencode"
	"github.com/ctxloom/ctxloom/internal/paths"
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
	// newInstanceConfig constructs the backend's INSTANCE-CONFIG writer — the
	// engine-owned generator of its own top-level config file inside a config
	// home ctxloom provisioned (claude's .claude.json, codex's config.toml
	// base). It is the write-config half of the engine capability set, sibling
	// to newWriter, and exists because config-file manipulation is an ENGINE
	// capability: the ambient copy-in decides WHICH files cross from the user's
	// real host home, the engine owns every byte of its own format.
	//
	// Pushed into internal/lm/isolation by registerDescriptor because that
	// package resolves engines by NAME and cannot import these
	// ones. nil = the backend has no generated instance config at all; kiro is
	// NOT nil — it registers a DECLARED-EMPTY writer, so "contributes nothing"
	// is a fact about kiro rather than an inference from a missing entry.
	newInstanceConfig func(agent.SettingsOptions) agent.InstanceConfigWriter
	// newCredentialProjector constructs the backend's AMBIENT-CREDENTIAL
	// projector — the engine-owned transform applied to a COPY of one host
	// credential file as it crosses into an instance home (claude strips its
	// single-use rotating refresh token here). Sibling of newInstanceConfig, and
	// pushed into internal/lm/isolation the same way and for the same reason.
	// nil = the backend's ambient credential files copy VERBATIM; only claude
	// registers one today.
	newCredentialProjector func() agent.CredentialProjector
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
	// audited, no guard needed (opencode's global path never collapses onto
	// its project path — see operations.checkHookTargetScope's historical doc,
	// preserved there).
	hookGlobalScopePaths func(workDir string) (projectPath, globalPath string, err error)
	// hookGlobalScopeLabel is the human-facing name for this backend's global
	// scope, read into CheckHookTargetScope's refusal/warning message (e.g.
	// "Claude Code's user-global settings file"). Empty when
	// hookGlobalScopePaths is nil.
	hookGlobalScopeLabel string
	// inTreeAgentHome resolves the ctxloom-CONTROLLED config-home INSTANCE an
	// in-tree agent run of this backend gets for ONE session, keyed by
	// (project root, harp): which env var relocates the engine's home, where
	// that session's instance points, and how (if at all) it is prepared. nil =
	// this backend gets no controlled in-tree home; see InTreeAgentHomeFor
	// (delegate_seams.go) for the roster's deliberate absentees and for the
	// scoping rule operations applies on top.
	//
	// The error is harp validation, surfaced by the engine package's own
	// paths.SessionHomePath call — an instance cannot be named without a valid
	// session, which is what keeps a durable project-wide home from regrowing.
	//
	// It lives here, beside hookGlobalScopePaths and resolveModel, for the same
	// ADR-0026 reason: the fact is engine-specific but the CALLER
	// (internal/operations) must not branch on engine identity or import a
	// concrete engine package to learn it.
	inTreeAgentHome func(workDir, harp string) (InTreeAgentHomeSpec, error)
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
	// versionCommand declares how to ask THIS engine's binary for its own
	// version — the flag(s) to pass and how to read the answer out of what it
	// prints (see engineversion.go for the three measured output shapes and
	// why one shared regex would be a guess). It belongs here, with the other
	// per-engine facts, rather than in a switch inside the prober.
	//
	// Read by VersionCommandFor/ResolveEngineVersionCommand, which feed
	// internal/engineversion's cached Prober; the probed version is recorded
	// on the session at start (sessions.Entry.EngineVersion) and is what
	// selects a vendor transcript reader later. The zero value (nil Parse)
	// means "this engine cannot be asked" — correct for mock (no binary) and
	// the generic acp backend (whatever command config names), and a
	// REFUSAL-CAUSING gap for any engine whose transcripts ctxloom reads.
	versionCommand engineversion.Command
	// launchOnlySettingsReason declares, in one clause, that this backend's
	// settings/prompt/skill surfaces exist ONLY inside a per-session engine
	// home, so no stable path a STATIC materialize/apply can write exists at
	// all. Empty for every backend whose settings live at a cwd-keyed project
	// path (claude-code, kiro, opencode) — codex is the only one, because it is
	// the only engine with no cwd-keyed equivalent of .claude/settings.json.
	//
	// It is the third member of the declared-absence family beside
	// noHooksReason and unsupportedHookKinds, and it is declared for the
	// identical reason: a surface written nowhere is indistinguishable from a
	// surface nobody asked for, so the absence has to be DECLARED to be
	// reportable. Read by LaunchOnlySurfaces (surfaces.go), which materialize
	// folds into its "not carried" report.
	//
	// DELIBERATELY NOT read by UncarriedSurfaces. That one answers "what can
	// this ENGINE never carry", and is consulted by `agent show` about a live
	// binding — where an agent declaring `config_home: project` DOES get its
	// hooks, at launch. Reporting them lost there would be a false alarm about
	// a run that works. This field answers the narrower "what can a HARPLESS
	// caller not write", which is a fact about the caller, not the engine.
	launchOnlySettingsReason string
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

// descriptors holds the per-agent descriptor table, keyed by CANONICAL backend
// name (agent.CanonicalEngineName): registration asserts it, and lookup below
// is the only read path, so the key side and the read side cannot drift.
var descriptors = make(map[string]*agentDescriptor)

// lookup resolves name to its descriptor through the repo-wide alias table
// (agent.CanonicalEngineName), so every spelling that resolves at ltk and at
// taskloom resolves to the same backend here.
//
// The registry read is the chokepoint that can hold this invariant, not any
// single entry boundary: an engine name reaches this package from CLI flags,
// from decoded config entries, from stored agent definitions and from MCP tool
// arguments, and no boundary is common to all of them. It is also the shape
// ltkengine.Get and taskloom/engine.Get already have, which is what makes one
// engine name mean one thing across all three binaries.
//
// No fuzzy matching: an unrecognized name arrives here lowercased and
// unresolved, so the caller still refuses it rather than rounding it to a real
// backend.
func lookup(name string) (*agentDescriptor, bool) {
	d, ok := descriptors[agent.CanonicalEngineName(name)]
	return d, ok
}

// registerDescriptor installs a backend's complete descriptor. Panics on a
// duplicate name: this only ever runs at init() time from the
// literal calls below, so a collision is a programming error (a new backend
// accidentally reusing an existing name) — silently letting the second
// registration win would drop the first's writer/surfaces/exports with no
// signal at all.
func registerDescriptor(d agentDescriptor) {
	// A descriptor registered under a name the alias table would rewrite would
	// be keyed where no lookup can reach it, which reads exactly like the
	// backend having no capabilities at all.
	if canonical := agent.CanonicalEngineName(d.name); canonical != d.name {
		panic("backends: descriptor name " + d.name + " is not canonical (want " + canonical + ")")
	}
	if _, dup := descriptors[d.name]; dup {
		panic("backends: duplicate descriptor registration for " + d.name)
	}
	descriptors[d.name] = &d
	if d.newInstanceConfig != nil {
		// Push the engine's own config writer down to internal/lm/isolation at
		// the same moment the descriptor is registered, so a backend can never
		// be launchable here while invisible there. isolation resolves engines
		// by NAME (its worktree axis has no engine value in hand) and cannot
		// import these packages, so this is the only direction the wiring can
		// run — see isolation.RegisterInstanceConfigWriter.
		isolation.RegisterInstanceConfigWriter(d.name, d.newInstanceConfig(agent.SettingsOptions{}))
	}
	if d.newCredentialProjector != nil {
		// Same push, same reason as the instance-config writer above: isolation
		// resolves engines by NAME and cannot import these packages, so the
		// engine-owned credential projector is registered here at descriptor time.
		isolation.RegisterCredentialProjector(d.name, d.newCredentialProjector())
	}
}

// prepareInTreeAmbient is the Prepare body every in-tree config-home descriptor
// shares: THE ambient copy-in (isolation.CopyAmbient) into this session's
// instance root, turning its "nothing seedable" DECISION into the actionable
// error operations.InTreeAgentHomeEnv fails loud on.
//
// The decision is not an error inside CopyAmbient because the two axes answer
// it differently — this one refuses the relocation outright rather than point
// an engine at a home it cannot authenticate against; the worktree axis records
// a degradable ClassIsolation finding and carries on.
func prepareInTreeAmbient(engine, instanceRoot, workDir string) error {
	report, err := isolation.CopyAmbient(isolation.AmbientRequest{
		Engine:       engine,
		InstanceHome: instanceRoot,
		WorkDir:      workDir,
	})
	if err != nil {
		return err
	}
	if report.NoSource {
		return fmt.Errorf("%s", report.NoSourceReason)
	}
	return nil
}

// descriptorFor returns the named descriptor, creating an empty one if absent.
// It backs RegisterHookGlobalScopeForTesting (delegate_seams.go) — a test-only
// piecemeal registration seam. The piecemeal Register/RegisterConfig entry
// points it originally backed were deleted as dead (zero callers
// anywhere, repo-wide); built-in backends register their complete descriptor
// via registerDescriptor (below) in one call.
func descriptorFor(name string) *agentDescriptor {
	canonical := agent.CanonicalEngineName(name)
	d, ok := descriptors[canonical]
	if !ok {
		d = &agentDescriptor{name: canonical}
		descriptors[canonical] = d
	}
	return d
}

// Get returns a new instance of the named backend.
func Get(name string) agent.Backend {
	if d, ok := lookup(name); ok && d.newBackend != nil {
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
	d, ok := lookup(name)
	return ok && d.newBackend != nil
}

// EnforcesReadOnlyPlan reports whether the named backend maps
// agent.PermissionPlan to a genuinely read-only, non-prompting mode (claude
// --permission-mode plan, codex --sandbox read-only, opencode.json permission
// {edit:deny, bash:deny}, kiro --trust-tools=fs_read). A backend that doesn't
// (acp) would run plan unrestrained and can't be trusted to be headless-safe
// for it, so the run resolver collapses plan to default for it instead. An
// unregistered name reports false.
func EnforcesReadOnlyPlan(name string) bool {
	d, ok := lookup(name)
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
	d, ok := lookup(name)
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
	// parked_engines: kiroACPTransport/opencodeACPTransport served only the
	// kiro/opencode registerDescriptor blocks below, both commented out.
	// kiroACPTransport: kiro-cli speaks ACP natively (`kiro-cli acp` —
	// internal/kiro/chat.go) — no separate adapter binary.
	// kiroACPTransport = agent.ACPTransport{Kind: agent.ACPNative}
	// opencodeACPTransport: opencode speaks ACP natively (`opencode acp` —
	// internal/opencode/chat.go) — no separate adapter binary.
	// opencodeACPTransport = agent.ACPTransport{Kind: agent.ACPNative}
	// acpGenericACPTransport: the generic "acp" backend drives WHATEVER
	// ACP-speaking command config supplies (`command: "kiro-cli acp"`,
	// `claude-code-acp`, ...) — from this backend's own point of view that
	// command is a native passthrough, not an adapter it manages; provenance
	// vetting for a THIRD-PARTY command configured here is the user's own
	// job, same posture as any other config value.
	acpGenericACPTransport = agent.ACPTransport{Kind: agent.ACPNative}
)

// Every backend registered here reaches its model by spawning the VENDOR'S OWN agent
// binary (claude, codex, kiro-cli) or that vendor's ACP adapter. ctxloom holds no
// provider SDK and makes no direct call to any model API — and must not acquire one on
// any path that carries subscription credentials.
//
// This is a licensing invariant, not a style preference. Anthropic reserves subscription
// OAuth for "ordinary use of Claude Code and other native Anthropic applications", bars
// tools that "misrepresent their identity to Anthropic's servers" or "route third-party
// traffic against subscription limits", and directs anyone building on the Agent SDK to
// API keys instead (support article 13189465; code.claude.com
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
		newWriter:              claude.NewWriter,
		newInstanceConfig:      claude.NewInstanceConfigWriter,
		newCredentialProjector: claude.NewCredentialProjector,
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
		versionCommand:       engineversion.Command{Args: []string{"--version"}, Parse: parseClaudeCodeVersion},
		// claude's project settings.json (claude.ProjectSettingsPath) collapses
		// onto its user-global one (claude.GlobalSettingsPath) exactly when
		// workDir == $HOME — found live (`manage
		// hooks install` run from $HOME silently went global).
		hookGlobalScopePaths: func(workDir string) (string, string, error) {
			global, err := claude.GlobalSettingsPath()
			return claude.ProjectSettingsPath(workDir), global, err
		},
		hookGlobalScopeLabel: "Claude Code's user-global settings file",
		// An in-tree agent run whose binding declares `config_home: project`
		// gets THIS SESSION's own CLAUDE_CONFIG_DIR, copy-seeded with the
		// host's .credentials.json, instead of the human's own ~/.claude. The
		// session's instance ROOT (paths.SessionHomePath) is what
		// PrepareClaudeHome is handed, not the claude leaf: it joins the seed
		// spec's own copy of that leaf under what it is given, landing on
		// claude.SessionConfigDir exactly —
		// TestSessionConfigDir_IsTheSeedDestination is the gate.
		inTreeAgentHome: func(workDir, harp string) (InTreeAgentHomeSpec, error) {
			dir, err := claude.SessionConfigDir(workDir, harp)
			if err != nil {
				return InTreeAgentHomeSpec{}, err
			}
			root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harp)
			if err != nil {
				return InTreeAgentHomeSpec{}, err
			}
			return InTreeAgentHomeSpec{
				EnvVar: claude.ConfigDirEnv,
				Dir:    dir,
				Prepare: func() error {
					return prepareInTreeAmbient("claude-code", root, workDir)
				},
			}, nil
		},
	})

	// 	// LIVE-UNTESTED: codex has never been run against a real account on any
	// 	// dev host (see the package doc in internal/codex for what's proven vs
	// 	// unverified; taskloom bold-smirk tracks the revive).
	// 	registerDescriptor(agentDescriptor{
	// 		name: "codex",
	// 		newBackend: func() agent.Backend {
	// 			b := codex.NewCodex() // sets its own ACPTransport intrinsically
	// 			b.SetLauncher(RunLaunchSpec)
	// 			return b
	// 		},
	// 		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
	// 			return decodeBody(body, &codex.CodexConfig{})
	// 		},
	// 		newWriter:         codex.NewWriter,
	// 		newInstanceConfig: codex.NewInstanceConfigWriter,
	// 		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet {
	// 			// The static apply/materialize path has no isolation context — no
	// 			// homeOverride/trustAbsPath, exactly as before those params existed
	// 			// (the live run/launch path wires them via Codex.buildSurfaces
	// 			// instead, which does not go through this registry closure).
	// 			return codex.NewSurfaces(in, "", "", fs)
	// 		},
	// 		exports:              codexExports,
	// 		skillExports:         codexSkillExports,
	// 		enforcesReadOnlyPlan: true, // plan → --sandbox read-only (both subcommands; see codex.buildArgs)
	// 		acpTransport:         codex.CodexACPTransport,
	// 		versionCommand:       engineversion.Command{Args: []string{"--version"}, Parse: parseCodexVersion},
	// 		// codex has hooks generally (unlike opencode, so noHooksReason stays
	// 		// empty) but no native session_end event — see codex.NoSessionEndReason
	// 		// and addUnifiedHooks' route for the write-time half of this same fact.
	// 		unsupportedHookKinds: map[string]string{
	// 			bundles.HookEventSessionEnd: codex.NoSessionEndReason,
	// 		},
	// 		// S7's DECLARED ABSENCE. codex reads hooks, MCP servers, prompts and
	// 		// skills only from $CODEX_HOME, which since S5 is either a per-session
	// 		// instance ctxloom creates at launch or the user's own ~/.codex, which
	// 		// ctxloom never writes — so a harpless materialize/install has no
	// 		// target at all. Stated once, in internal/codex, and read here so a
	// 		// caller that never imports that package reports the identical
	// 		// sentence.
	// 		launchOnlySettingsReason: codex.LaunchOnlySettingsReason,
	// 		// hookGlobalScopePaths is deliberately ABSENT (audited, not
	// 		// overlooked). Its purpose is the workDir == $HOME collision, where a
	// 		// backend's PROJECT config path collapses onto its user-global one —
	// 		// claude's and kiro's still do. codex no longer HAS a project config
	// 		// path (the declared absence above), so the static path writes nothing
	// 		// that could land in the user's global home; and the run path has its
	// 		// own, stronger guard — codex.IsHostCodexHome refuses the real home
	// 		// outright, whatever the workDir.
	// 		// D2 (RULED): codex reads config_home like claude and kiro,
	// 		// through THIS seam and no other. An in-tree run whose binding declares
	// 		// `config_home: project` gets this session's own CODEX_HOME,
	// 		// copy-seeded with the host's auth.json; every other in-tree run keeps
	// 		// the real ~/.codex (internal/codex's resolveCodexProjectDir,
	// 		// codexHomeRealHost). codex used to relocate CODEX_HOME here
	// 		// unconditionally, which stopped being defensible the moment the
	// 		// relocation target became a DISPOSABLE per-session instance: an
	// 		// unbound interactive run would have lost its token refreshes and its
	// 		// codex state every session.
	// 		//
	// 		// The env value is the session instance root plus codex's own
	// 		// ConfigDirName, because CODEX_HOME IS the .codex directory rather
	// 		// than its parent — the same composition
	// 		// isolation's credentialSeedSpecs["codex"] HomeVar Subdir performs for
	// 		// the worktree axis, and the suffix resolveCodexProjectDir strips back
	// 		// off to recover the virtual project dir. Prepare is handed the ROOT,
	// 		// which is what PrepareCodexHome joins ".codex" under.
	// 		inTreeAgentHome: func(workDir, harp string) (InTreeAgentHomeSpec, error) {
	// 			root, err := codex.SessionHome(workDir, harp)
	// 			if err != nil {
	// 				return InTreeAgentHomeSpec{}, err
	// 			}
	// 			return InTreeAgentHomeSpec{
	// 				EnvVar: codex.CodexHomeEnv,
	// 				Dir:    filepath.Join(root, codex.ConfigDirName),
	// 				Prepare: func() error {
	// 					return prepareInTreeAmbient("codex", root, workDir)
	// 				},
	// 			}, nil
	// 		},
	// 	})

	// 	// Kiro (direct-CLI path via `kiro-cli chat`). Materializes native config the
	// 	// agent reads from cwd: the ctxloom agent (.kiro/agents/ctxloom.json — hooks +
	// 	// skill resources), MCP (.kiro/settings/mcp.json), context (.kiro/steering/),
	// 	// commands AND Agent Skills, both under .kiro/skills/<n>/SKILL.md — the one
	// 	// engine where those two surfaces collide (D6 skill-wins, see
	// 	// kiro.filterClaimedCommands in kiro/surfaces.go).
	// 	// LIVE-VERIFIED against an authenticated kiro-cli — see the package doc in
	// 	// internal/kiro for exactly what was proven (backend parity, a real oneshot
	// 	// chat, and --model honor confirmed two independent ways).
	// 	registerDescriptor(agentDescriptor{
	// 		name: "kiro",
	// 		newBackend: func() agent.Backend {
	// 			b := kiro.NewKiro()
	// 			b.SetLauncher(RunLaunchSpec)
	// 			b.SetACPTransport(kiroACPTransport)
	// 			return b
	// 		},
	// 		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
	// 			return decodeBody(body, &kiro.KiroConfig{})
	// 		},
	// 		newWriter:         kiro.NewWriter,
	// 		newInstanceConfig: kiro.NewInstanceConfigWriter,
	// 		newSurfaces:       func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return kiro.NewSurfaces(in, fs) },
	// 		exports:           kiroExports,
	// 		skillExports:      kiroSkillExports,
	// 		// LIVE VERIFIED (authenticated kiro-cli 2.12.1):
	// 		// `--trust-tools=fs_read` genuinely denies a headless fs_write — a
	// 		// sentinel-file overwrite left the file byte-unchanged and kiro-cli
	// 		// printed "Command fs_write is rejected because it matches one or
	// 		// more rules on the denied list". `--trust-tools=fs_read,fs_write`
	// 		// and `--trust-all-tools` (positive controls) both let the same write
	// 		// land. See kiro.buildArgs (backend.go) for the mapping.
	// 		enforcesReadOnlyPlan: true,
	// 		acpTransport:         kiroACPTransport,
	// 		versionCommand:       engineversion.Command{Args: []string{"--version"}, Parse: parseKiroVersion},
	// 		// kiro has hooks generally but no native session_end event — its only
	// 		// turn-boundary event is `stop`, which fires once per TURN. See
	// 		// kiro.NoSessionEndReason and mapHooks' route for the write-time half.
	// 		unsupportedHookKinds: map[string]string{
	// 			bundles.HookEventSessionEnd: kiro.NoSessionEndReason,
	// 		},
	// 		// kiro's project .kiro dir (kiro.ProjectHome) collapses onto its bare
	// 		// GLOBAL home (kiro.GlobalHome) exactly when workDir == $HOME --
	// 		// the same collision class found for claude.
	// 		hookGlobalScopePaths: func(workDir string) (string, string, error) {
	// 			global, err := kiro.GlobalHome()
	// 			return kiro.ProjectHome(workDir), global, err
	// 		},
	// 		hookGlobalScopeLabel: "kiro's global home",
	// 		// An in-tree AGENT run gets a project-scoped KIRO_HOME instead of the
	// 		// human's own ~/.kiro (which since kiro-cli 2.3.0 carries their global
	// 		// agents, prompts, skills, steering and settings, not just sessions).
	// 		// No Seed, and none possible: kiro's subscription auth lives in a global
	// 		// sqlite under XDG_DATA_HOME that KIRO_HOME does not relocate, so a
	// 		// FRESH home stays authenticated — and XDG_DATA_HOME is deliberately
	// 		// NOT relocated alongside it, since relocating a credential store with
	// 		// nothing to seed into it is what strands an agent logged out.
	// 		inTreeAgentHome: func(workDir, harp string) (InTreeAgentHomeSpec, error) {
	// 			dir, err := kiro.SessionHome(workDir, harp)
	// 			if err != nil {
	// 				return InTreeAgentHomeSpec{}, err
	// 			}
	// 			return InTreeAgentHomeSpec{EnvVar: kiro.HomeEnv, Dir: dir}, nil
	// 		},
	// 	})

	// ACP (generic Agent Client Protocol client): drives ANY ACP-capable agent
	// chosen by config (`command: "kiro-cli acp"`, `claude-code-acp`) — new ACP
	// agents become CONFIG, not code. Structured chat +
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

	// 	// opencode (first-party `opencode acp`, HOST-only chat spine). Slice 2 adds the
	// 	// settings/materialization seam: ctxloom's managed keys are merged into a
	// 	// project-local, strictly-validated opencode.json — MCP servers (`mcp`),
	// 	// assembled context (`instructions` -> .opencode/ctxloom-context.md), and, on the
	// 	// live chat path only, a GENUINE read-only `permission` for plan mode. Slice 3
	// 	// adds command (commands) materialization: enabled bundle prompts become
	// 	// opencode custom commands (.opencode/command/<name>.md), delivered by the
	// 	// commands surface on the static `profile materialize` path and transiently
	// 	// in Chat on the LIVE path (written before the run, reverted after — same
	// 	// no-debris shape as the opencode.json overlay). The newSurfaces builder
	// 	// serves materialize (mcp + context + commands).
	// 	// enforcesReadOnlyPlan is TRUE: the written permission denies edit (which gates
	// 	// opencode's write tool too) AND bash, so a plan run genuinely cannot mutate —
	// 	// stricter than opencode's built-in `plan` agent, which leaves bash allowed.
	// 	// Session-history and interactive PTY launch are later slices.
	// 	registerDescriptor(agentDescriptor{
	// 		name: "opencode",
	// 		newBackend: func() agent.Backend {
	// 			b := opencode.NewOpencode()
	// 			b.SetLauncher(RunLaunchSpec)
	// 			b.SetACPTransport(opencodeACPTransport)
	// 			return b
	// 		},
	// 		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
	// 			return decodeBody(body, &opencode.OpencodeConfig{})
	// 		},
	// 		newWriter:            opencode.NewWriter,
	// 		newSurfaces:          func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return opencode.NewSurfaces(in, fs) },
	// 		exports:              opencodeExports,
	// 		skillExports:         opencodeSkillExports,
	// 		enforcesReadOnlyPlan: true, // plan -> opencode.json permission {edit:deny, bash:deny}
	// 		acpTransport:         opencodeACPTransport,
	// 		versionCommand:       engineversion.Command{Args: []string{"--version"}, Parse: parseOpencodeVersion},
	// 		// opencode is the one backend with no hooks surface of any shape:
	// 		// opencode.json has no hook key, there is no settings event vocabulary
	// 		// to route the seven unified events onto, and OpencodeWriter.WriteSettings
	// 		// accepts a *wire.HooksConfig it cannot do anything with. Declared here
	// 		// so `profile materialize` can SAY so instead of writing four true
	// 		// "wrote" lines over a silently dropped guardrail.
	// 		noHooksReason: "opencode has no hook mechanism",
	// 	})

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
		// mock is the SAME structural shape as opencode here: mockPresentations
		// (mock_surfaces.go) declares context and skills, so a configured
		// session_start hook has no settings surface to land on and reaches no
		// file. Declared for the same reason opencode's noHooksReason is —
		// UncarriedSurfaces can only report a loss that is DECLARED, and an
		// undeclared one reads as silence. When mock gains a
		// settings/hook surface (tracked separately), delete this line; the
		// hook sentinel will then need a real destination instead.
		noHooksReason: "mock has no settings/hook surface",
	})
}
