package ai

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
		return nil, fmt.Errorf("ai plugin not found: %s", name)
	}
	return p, nil
}

// GetWithConfig retrieves a plugin by name and configures it.
func GetWithConfig(name string, cfg PluginConfig) (Plugin, error) {
	p, err := Get(name)
	if err != nil {
		return nil, err
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
