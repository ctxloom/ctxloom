package backends

import (
	"os/exec"

	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
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
// capability-specific dispatch: a nil newWriter means the backend doesn't
// support settings (WriteSettings no-ops, BackendsWithSettings omits it); nil
// exports/writeCommands mean no slash-command export (WriteCommandFilesFor
// and commandExportsFor no-op). The mock backend registers only
// backend+config.
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
	// exports maps loaded bundle content to this backend's command exports,
	// resolving its per-prompt enablement + metadata. nil = no command export.
	// Shared by WriteCommandFilesFor and commandExportsFor so the two paths
	// can't diverge.
	exports func([]*bundles.LoadedContent) []agent.CommandExport
	// writeCommands writes the backend's slash-command files (the module's
	// WriteCommandFiles). nil = no command export.
	writeCommands func(string, []agent.CommandExport, ...agent.CommandFileOption) error
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
			b := claude.NewClaudeCode(WriteSettings)
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &claude.ClaudeConfig{})
		},
		newWriter:     claude.NewWriter,
		exports:       claudeExports,
		writeCommands: claude.WriteCommandFiles,
	})

	registerDescriptor(agentDescriptor{
		name: "antigravity",
		newBackend: func() agent.Backend {
			b := antigravity.NewAntigravity(WriteSettings)
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &antigravity.AntigravityConfig{})
		},
		newWriter:     antigravity.NewWriter,
		exports:       antigravityExports,
		writeCommands: antigravity.WriteCommandFiles,
	})

	registerDescriptor(agentDescriptor{
		name: "codex",
		newBackend: func() agent.Backend {
			b := codex.NewCodex(WriteSettings)
			b.SetLauncher(RunLaunchSpec)
			return b
		},
		decodeConfig: func(body map[string]interface{}) (agent.BackendConfig, error) {
			return decodeBody(body, &codex.CodexConfig{})
		},
		newWriter:     codex.NewWriter,
		exports:       codexExports,
		writeCommands: codex.WriteCommandFiles,
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
