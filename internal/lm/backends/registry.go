package backends

import (
	"os/exec"

	"github.com/ctxloom/claude"
	"github.com/ctxloom/codex"
	"github.com/ctxloom/gemini"
	"github.com/ctxloom/shared/agent"
)

// Configurable is implemented by backends that accept their own typed config.
// The argument is the backend's concrete BackendConfig (decoded by the config
// registry), so no shared code ever type-switches on backend specifics.
type Configurable interface {
	Configure(cfg agent.BackendConfig)
}

// registry holds all registered backends.
var registry = make(map[string]func() agent.Backend)

// Register adds a backend constructor to the registry.
func Register(name string, constructor func() agent.Backend) {
	registry[name] = constructor
}

// Get returns a new instance of the named backend.
func Get(name string) agent.Backend {
	if constructor, ok := registry[name]; ok {
		return constructor()
	}
	return nil
}

// List returns all registered backend names.
func List() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// Exists returns true if a backend with the given name is registered.
func Exists(name string) bool {
	_, ok := registry[name]
	return ok
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
	// Register all built-in backends along with their config decoders. The
	// decoder turns a labeled entry's raw body into the backend's typed config.
	// Each local-CLI backend gets ctxloom's pty-backed launcher injected — the
	// substrate no longer execs processes itself.
	Register("claude-code", func() agent.Backend {
		b := claude.NewClaudeCode(WriteSettings)
		b.SetLauncher(RunLaunchSpec)
		return b
	})
	RegisterConfig("claude-code", func(body map[string]interface{}) (agent.BackendConfig, error) {
		return decodeBody(body, &claude.ClaudeConfig{})
	})

	Register("gemini", func() agent.Backend {
		b := gemini.NewGemini(WriteSettings)
		b.SetLauncher(RunLaunchSpec)
		return b
	})
	RegisterConfig("gemini", func(body map[string]interface{}) (agent.BackendConfig, error) {
		return decodeBody(body, &gemini.GeminiConfig{})
	})

	Register("codex", func() agent.Backend {
		b := codex.NewCodex(WriteSettings)
		b.SetLauncher(RunLaunchSpec)
		return b
	})
	RegisterConfig("codex", func(body map[string]interface{}) (agent.BackendConfig, error) {
		return decodeBody(body, &codex.CodexConfig{})
	})

	Register("mock", func() agent.Backend { return NewMock() })
	RegisterConfig("mock", func(body map[string]interface{}) (agent.BackendConfig, error) {
		return decodeBody(body, &MockConfig{})
	})
}
