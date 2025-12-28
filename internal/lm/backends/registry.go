package backends

import (
	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
)

// Backend is the interface that all AI backend implementations must satisfy.
// This matches the grpc.Backend interface.
type Backend = pb.Backend

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
