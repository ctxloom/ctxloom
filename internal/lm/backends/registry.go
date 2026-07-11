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
// yields nil, so the skills surface has nothing to write). The mock backend
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
	// Read by commandExportsFor / CommandExportsFor, which feed the skills surface
	// (SurfaceInputs.Skills) the enabled exports for the delivery seam.
	exports func([]*bundles.LoadedContent) []agent.CommandExport
	// enforcesReadOnlyPlan is true when the backend maps agent.PermissionPlan to a
	// genuinely read-only, non-prompting mode (see the backend's buildArgs plan
	// branch). false backends have no read-only tier, so plan would run
	// unrestrained — the run resolver collapses plan to default for them. Keep in
	// sync with the buildArgs plan mapping when a backend gains/loses the mode.
	enforcesReadOnlyPlan bool
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
// --permission-mode plan, codex --sandbox read-only). Backends that don't
// (antigravity, kiro, acp) would run plan unrestrained and can't be trusted to
// be headless-safe for it, so the run resolver collapses plan to default for
// them. An unregistered name reports false.
func EnforcesReadOnlyPlan(name string) bool {
	d, ok := descriptors[name]
	return ok && d.enforcesReadOnlyPlan
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
		newWriter: claude.NewWriter,
		// claude's NewSurfaces adapts the shared inputs to its LOCAL SurfaceInputs
		// and binds an out-of-cwd placement for the race-safe variants; the
		// well-known Deliveries() path materialize drives never dereferences it, so
		// a wellKnownPlacement is fine.
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet {
			return claude.NewSurfaces(claude.SurfaceInputs{
				Context:             in.Context,
				MCP:                 in.MCP,
				BundleMCP:           in.BundleMCP,
				Hooks:               in.Hooks,
				ManageStatusline:    in.ManageStatusline,
				Skills:              in.Skills,
				SelfContainedSkills: in.SelfContainedSkills,
			}, wellKnownPlacement{}, fs)
		},
		exports:              claudeExports,
		enforcesReadOnlyPlan: true, // --permission-mode plan is read-only
	})

	registerDescriptor(agentDescriptor{
		name: "antigravity",
		newBackend: func() agent.Backend {
			b := antigravity.NewAntigravity()
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &antigravity.AntigravityConfig{})
		},
		newWriter:   antigravity.NewWriter,
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return antigravity.NewSurfaces(in, fs) },
		exports:     antigravityExports,
	})

	// LIVE-UNTESTED: codex has never been run against a real account on any
	// dev host (see the package doc in internal/codex for what's proven vs
	// unverified; taskloom bold-smirk tracks the revive).
	registerDescriptor(agentDescriptor{
		name: "codex",
		newBackend: func() agent.Backend {
			b := codex.NewCodex()
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &codex.CodexConfig{})
		},
		newWriter:            codex.NewWriter,
		newSurfaces:          func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return codex.NewSurfaces(in, fs) },
		exports:              codexExports,
		enforcesReadOnlyPlan: true, // --sandbox read-only --ask-for-approval never
	})

	// Kiro (direct-CLI path via `kiro-cli chat`). Materializes native config the
	// agent reads from cwd: the ctxloom agent (.kiro/agents/ctxloom.json — hooks +
	// skill resources), MCP (.kiro/settings/mcp.json), context (.kiro/steering/),
	// skills (.kiro/skills/<n>/SKILL.md).
	// LIVE-UNTESTED: never run against a logged-in kiro-cli on any dev host
	// (see the package doc in internal/kiro for what's proven vs unverified;
	// taskloom numb-panda / bold-smirk track the revive).
	registerDescriptor(agentDescriptor{
		name: "kiro",
		newBackend: func() agent.Backend {
			b := kiro.NewKiro()
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &kiro.KiroConfig{})
		},
		newWriter:   kiro.NewWriter,
		newSurfaces: func(in agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet { return kiro.NewSurfaces(in, fs) },
		exports:     kiroExports,
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
		name:       "acp",
		newBackend: func() agent.Backend { return acp.NewACP() },
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &acp.ACPConfig{})
		},
		// A GENERIC ACP agent has no known native config format to materialize, so
		// it opts out with an empty surface set (mirrors its nil settings writer).
		newSurfaces: func(agent.SurfaceInputs, afero.Fs) agent.SurfaceSet { return agent.EmptySurfaceSet{} },
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
