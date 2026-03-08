package backends

import (
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

func init() {
	// Register all built-in backends
	Register("claude-code", func() Backend { return NewClaudeCode() })
	Register("gemini", func() Backend { return NewGemini() })
	Register("aider", func() Backend { return NewAider() })
	Register("cline", func() Backend { return NewCline() })
	Register("codex", func() Backend { return NewCodex() })
	Register("goose", func() Backend { return NewGoose() })
	Register("q", func() Backend { return NewQDeveloper() })
	Register("mock", func() Backend { return NewMock() })
}
