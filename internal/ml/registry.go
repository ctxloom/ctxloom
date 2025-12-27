package ml

import (
	"fmt"
	"sync"
)

var (
	registry = make(map[string]Plugin)
	mu       sync.RWMutex
)

// Register adds a plugin to the registry.
func Register(p Plugin) {
	mu.Lock()
	defer mu.Unlock()
	registry[p.Name()] = p
}

// Get retrieves a plugin by name.
func Get(name string) (Plugin, error) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("ml plugin not found: %s", name)
	}
	return p, nil
}

// GetWithConfig retrieves a plugin by name and configures it.
// If the plugin is cloneable, a new instance is returned to ensure thread-safety.
// Otherwise, the shared instance is configured in place (not thread-safe).
func GetWithConfig(name string, cfg PluginConfig) (Plugin, error) {
	mu.RLock()
	p, ok := registry[name]
	mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("ml plugin not found: %s", name)
	}

	// Clone the plugin if possible to avoid shared state mutations
	if cloneable, ok := p.(CloneablePlugin); ok {
		p = cloneable.Clone()
	}

	// If plugin supports configuration, apply it
	if configurable, ok := p.(ConfigurablePlugin); ok {
		configurable.Configure(cfg)
	}

	return p, nil
}

// List returns the names of all registered plugins.
func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// Default returns the default plugin name.
func Default() string {
	return "claude-code"
}
