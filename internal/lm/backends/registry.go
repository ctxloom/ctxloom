package backends

import (
	"os/exec"

	"github.com/SophisticatedContextManager/scm/internal/config"
)

// Configurable is an interface for backends that can be configured with plugin settings.
type Configurable interface {
	Configure(cfg *config.PluginConfig)
}

// ApplyPluginConfig applies plugin configuration to a backend.
// This sets binary path, args, and env.
func ApplyPluginConfig(backend Backend, cfg *config.PluginConfig) {
	if cfg == nil {
		return
	}
	if configurable, ok := backend.(Configurable); ok {
		configurable.Configure(cfg)
	}
}

// registry holds all registered backends.
var registry = make(map[string]func() Backend)

// Register adds a backend constructor to the registry.
func Register(name string, constructor func() Backend) {
	registry[name] = constructor
}

// Get returns a new instance of the named backend.
func Get(name string) Backend {
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
type BinaryPathProvider interface {
	GetBinaryPath() string
}

// GetBinaryPath returns the binary path from BaseBackend.
// This implements BinaryPathProvider.
func (b *BaseBackend) GetBinaryPath() string {
	return b.BinaryPath
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

// ContextFileName returns the context file name for a backend, or empty string if not found.
func ContextFileName(name string) string {
	backend := Get(name)
	if backend == nil {
		return ""
	}
	return backend.ContextFileName()
}

// ContextFileNames returns a map of all registered backend names to their context file names.
// Backends with empty context file names are excluded.
func ContextFileNames() map[string]string {
	result := make(map[string]string)
	for name := range registry {
		backend := Get(name)
		if backend != nil {
			if fileName := backend.ContextFileName(); fileName != "" {
				result[name] = fileName
			}
		}
	}
	return result
}

func init() {
	// Register all built-in backends
	Register("claude-code", func() Backend { return NewClaudeCode() })
	Register("gemini", func() Backend { return NewGemini() })
	Register("codex", func() Backend { return NewCodex() })
	Register("mock", func() Backend { return NewMock() })
}
