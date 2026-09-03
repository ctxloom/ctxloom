package backends

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/engineversion"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
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
	// home ctxloom provisioned (claude's .claude.json, for instance). It is
	// the write-config half of the engine capability set, sibling
	// to newWriter, and exists because config-file manipulation is an ENGINE
	// capability: the ambient copy-in decides WHICH files cross from the user's
	// real host home, the engine owns every byte of its own format.
	//
	// Pushed into internal/lm/isolation by registerDescriptor because that
	// package resolves engines by NAME and cannot import these
	// ones. nil = the backend has no generated instance config at all. A
	// backend that contributes nothing can still register a DECLARED-EMPTY
	// writer, so "contributes nothing" stays a stated fact rather than an
	// inference from a missing entry.
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
	// It is the delivery-seam counterpart of newWriter. nil = backend
	// materializes no surfaces; BuildSurfaces then returns an
	// EmptySurfaceSet. mock is NOT in that set: it registers a
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
	// export. Read by SkillExportsFor, the skills-surface analog
	// of CommandExportsFor.
	skillExports func([]*bundles.LoadedSkill) []agent.SkillExport
	// enforcesReadOnlyPlan is true when the backend maps agent.PermissionPlan to a
	// genuinely read-only, non-prompting mode (see the backend's buildArgs plan
	// branch). false backends have no read-only tier, so plan would run
	// unrestrained — the run resolver collapses plan to default for them. Keep in
	// sync with the buildArgs plan mapping when a backend gains/loses the mode.
	enforcesReadOnlyPlan bool
	// resolveModel translates a configured model string into the concrete id
	// this backend's launch path requires, returning ok=false when the given
	// model cannot be resolved to anything that path accepts. nil = the
	// backend's model passes through untouched, which is every backend today:
	// the engines are driven through their own CLIs, which accept the same
	// model spellings a user configures. Read by ResolveModelFor
	// (delegate_seams.go), the polymorphic replacement for operations' old
	// claude-only branch (ADR-0026). The seam is kept because the decision is
	// per-backend: an engine whose launch path rejects a spelling its config
	// accepts needs exactly this hook rather than a caller-side special case.
	resolveModel func(model string) (resolved string, ok bool)
	// hookGlobalScopePaths resolves this backend's project-scoped config path
	// (under a workDir) and its bare user-GLOBAL path, for backends that carry
	// a project/global collision class `manage hooks install` must guard
	// against (see the claude-code descriptor below for the collision itself,
	// and CheckHookTargetScope in delegate_seams.go for how it's used). nil =
	// audited, no guard needed — a backend whose global path never collapses
	// onto its project path (see operations.checkHookTargetScope's historical
	// doc, preserved there).
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
	// mechanism AT ALL and says why. Empty means the backend carries hooks.
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
	// means "this engine cannot be asked" — correct for mock (no binary), and
	// a REFUSAL-CAUSING gap for any engine whose transcripts ctxloom reads.
	versionCommand engineversion.Command
	// launchOnlySettingsReason declares, in one clause, that this backend's
	// settings/prompt/skill surfaces exist ONLY inside a per-session engine
	// home, so no stable path a STATIC materialize/apply can write exists at
	// all. Empty for every backend whose settings live at a cwd-keyed project
	// path; set only by a backend with no cwd-keyed equivalent of
	// .claude/settings.json.
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
	// string a HookRoute.Kind declares at write time, valued with that SAME
	// Unsupported reason, so UncarriedSurfaces can report the identical loss
	// to a caller that never writes settings (doctor/agent show)
	// without hand-maintaining a second copy of either string. nil = every
	// kind this backend's mechanism carries is natively supported.
	unsupportedHookKinds map[string]string
	// testOnly marks a descriptor as a test/development double: registered in
	// the production table and reachable at runtime (`--llm mock`), but never
	// offered to a user as a choice.
	//
	// It is a PROPERTY of the registration, not a name a caller matches on: a
	// registration that declares what it IS cannot drift from the list of
	// names each caller remembers to skip.
	testOnly bool
}

// IsTestOnly reports whether name is a registered test/development double
// rather than a shippable engine. Every user-facing enumeration over List()
// filters through this, so registering a new double hides it everywhere at
// once instead of requiring each caller to learn its name.
//
// An unknown name is NOT test-only: callers distinguish "unknown engine" from
// "engine you may not pick" separately, and folding the two here would turn a
// typo into a silent omission.
func IsTestOnly(name string) bool {
	d, ok := lookup(name)
	return ok && d.testOnly
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
// --permission-mode plan, for instance). A backend that doesn't would run
// plan unrestrained
// and can't be trusted to be headless-safe for it, so the run resolver
// collapses plan to default for it instead. An unregistered name reports false.
func EnforcesReadOnlyPlan(name string) bool {
	d, ok := lookup(name)
	return ok && d.enforcesReadOnlyPlan
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

// Every backend registered here reaches its model by spawning the VENDOR'S OWN agent
// binary. ctxloom holds no
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
			b := claude.NewClaudeCode()
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

	// Mock registers the COMPLETE descriptor — backend, config, surfaces,
	// settings writer, command exports and skill exports. It is a full engine
	// with no real model behind it, not a partial one, and that is deliberate:
	// while mock delivered only some surfaces, fixtures quietly came to depend
	// on the gaps, and a gap depended upon is a gap that breaks something the
	// day it closes.
	registerDescriptor(agentDescriptor{
		name:       "mock",
		newBackend: func() agent.Backend { return NewMock() },
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &MockConfig{})
		},
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return NewMockSurfaces(in, fs) },
		newWriter:   NewMockSettingsWriter,
		exports:     mockExports,
		testOnly:    true,
		// Without this mapper SurfaceInputs.Skills is always empty for mock and
		// the skills surface above delivers nothing — a surface that exists,
		// reports success and writes zero bytes, which is precisely the
		// silent no-op the mock engine exists to catch in others.
		skillExports: mockSkillExports,
	})

	// The deliberately-LOSSY double. Identical to mock except for its
	// registered NAME and the one unified hook kind its descriptor declares
	// unsupported — which is what gives UncarriedSurfaces, and therefore
	// doctor's capability-loss check and `manage check`'s loss reporting, a
	// subject to report on.
	//
	// It is a SECOND double rather than a limitation bolted onto mock because
	// the two prove different things: mock proves the surface seam is
	// polymorphic (it must be complete), this one proves the seam REPORTS what
	// an engine cannot carry (it must be lossy). One double cannot be both.
	// See config.BackendMockLossy.
	registerDescriptor(agentDescriptor{
		name:       config.BackendMockLossy,
		newBackend: func() agent.Backend { return NewMockLossy() },
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &MockLossyConfig{})
		},
		newSurfaces:  func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return NewMockSurfaces(in, fs) },
		newWriter:    NewMockSettingsWriter,
		exports:      mockExports,
		skillExports: mockSkillExports,
		testOnly:     true,
		// TWO kinds, not one: a double that models a single missing event
		// cannot exercise a report that groups several, and both shapes exist
		// in the wild (codex lacked session_end while carrying hooks
		// generally). Each names its own reason so a report cannot attribute
		// one kind's absence to the other's cause.
		unsupportedHookKinds: map[string]string{
			"session_start": config.BackendMockLossy + " has no native session_start event",
			"session_end":   config.BackendMockLossy + " has no native session_end event",
		},
	})

	// The LAUNCH-DELIVERED double. See config.BackendMockLaunch for why a third
	// double rather than a flag on one of the other two.
	//
	// It keeps exports and skillExports even though it writes neither. That is
	// the point: those populate SurfaceInputs.Commands and .Skills, and
	// LaunchOnlySurfaces reports a surface only when the inputs actually
	// CARRIED something to deliver. Drop them and the double asks for nothing,
	// so nothing is reported missing, and the scenario passes because the
	// question was never posed — the shape of vacuous pass this repo keeps
	// finding.
	//
	// It DOES declare a settings writer, and that is the whole shape rather than
	// a concession to the table invariant: the writer is the LAUNCH-time
	// mechanism and the surface is the MATERIALIZE-time one. This engine has no
	// materialize surface for settings precisely BECAUSE its writer runs at
	// launch, into the per-session home. An engine with neither would not be
	// launch-delivered; it would simply have no settings.
	registerDescriptor(agentDescriptor{
		name:       config.BackendMockLaunch,
		newBackend: func() agent.Backend { return NewMockLaunch() },
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &MockLaunchConfig{})
		},
		newSurfaces:  func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return NewMockLaunchSurfaces(in, fs) },
		newWriter:    NewMockSettingsWriter,
		exports:      mockExports,
		skillExports: mockSkillExports,
		testOnly:     true,
		// The clause has to SAY WHERE THEY COME FROM, not merely that they were
		// not written. "not carried" alone reads as this engine losing them;
		// "delivered per-session at launch" is the sentence that turns a
		// apparent loss into a narrowing the user can reason about.
		launchOnlySettingsReason: config.BackendMockLaunch +
			" keeps settings, MCP servers, commands and skills in a per-session engine home: they are delivered per-session at launch, which a static materialize has no home to write into",
	})
}
