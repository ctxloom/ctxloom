package ai

import (
	"context"
	"io"
	"sync"
	"testing"
)

// mockPlugin is a test plugin implementation.
type mockPlugin struct {
	name       string
	configured bool
	config     PluginConfig
}

func (m *mockPlugin) Name() string { return m.name }

func (m *mockPlugin) Run(ctx context.Context, req Request, stdout, stderr io.Writer) (*Response, error) {
	return &Response{Output: "mock output", ExitCode: 0}, nil
}

func (m *mockPlugin) Configure(cfg PluginConfig) {
	m.configured = true
	m.config = cfg
}

func (m *mockPlugin) Clone() Plugin {
	return &mockPlugin{
		name:       m.name,
		configured: m.configured,
		config:     m.config,
	}
}

func TestRegister(t *testing.T) {
	// Save and restore registry state
	oldRegistry := registry
	defer func() {
		registry = oldRegistry
	}()
	registry = make(map[string]Plugin)

	plugin := &mockPlugin{name: "test-plugin"}
	Register(plugin)

	if _, ok := registry["test-plugin"]; !ok {
		t.Error("expected plugin to be registered")
	}
}

func TestGet(t *testing.T) {
	// Save and restore registry state
	oldRegistry := registry
	defer func() {
		registry = oldRegistry
	}()
	registry = make(map[string]Plugin)

	plugin := &mockPlugin{name: "test-plugin"}
	Register(plugin)

	// Get existing plugin
	p, err := Get("test-plugin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "test-plugin" {
		t.Errorf("expected name 'test-plugin', got %q", p.Name())
	}

	// Get non-existent plugin
	_, err = Get("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent plugin")
	}
}

func TestGetWithConfig(t *testing.T) {
	// Save and restore registry state
	oldRegistry := registry
	defer func() {
		registry = oldRegistry
	}()
	registry = make(map[string]Plugin)

	original := &mockPlugin{name: "configurable"}
	Register(original)

	cfg := PluginConfig{
		BinaryPath: "/custom/path",
		Args:       []string{"--flag"},
	}

	p, err := GetWithConfig("configurable", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return a clone, not the original
	mock := p.(*mockPlugin)
	if mock == original {
		t.Error("expected a cloned instance, not the original")
	}

	// Clone should be configured
	if !mock.configured {
		t.Error("expected plugin to be configured")
	}
	if mock.config.BinaryPath != "/custom/path" {
		t.Errorf("expected binary path '/custom/path', got %q", mock.config.BinaryPath)
	}

	// Original should NOT be modified
	if original.configured {
		t.Error("original plugin should not be configured")
	}
}

func TestGetWithConfigConcurrency(t *testing.T) {
	// Save and restore registry state
	oldRegistry := registry
	defer func() {
		registry = oldRegistry
	}()
	registry = make(map[string]Plugin)

	original := &mockPlugin{name: "concurrent"}
	Register(original)

	var wg sync.WaitGroup
	results := make(chan *mockPlugin, 10)

	// Spawn multiple goroutines that get and configure the plugin
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cfg := PluginConfig{BinaryPath: "/path/" + string(rune('0'+id))}
			p, err := GetWithConfig("concurrent", cfg)
			if err != nil {
				t.Errorf("goroutine %d: unexpected error: %v", id, err)
				return
			}
			results <- p.(*mockPlugin)
		}(i)
	}

	wg.Wait()
	close(results)

	// Each result should be a unique instance
	seen := make(map[*mockPlugin]bool)
	for p := range results {
		if seen[p] {
			t.Error("expected unique plugin instances for each call")
		}
		seen[p] = true
	}

	// Original should remain unchanged
	if original.configured {
		t.Error("original plugin should not be modified by concurrent access")
	}
}

func TestList(t *testing.T) {
	// Save and restore registry state
	oldRegistry := registry
	defer func() {
		registry = oldRegistry
	}()
	registry = make(map[string]Plugin)

	Register(&mockPlugin{name: "plugin-a"})
	Register(&mockPlugin{name: "plugin-b"})

	names := List()
	if len(names) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(names))
	}

	nameMap := make(map[string]bool)
	for _, n := range names {
		nameMap[n] = true
	}
	if !nameMap["plugin-a"] || !nameMap["plugin-b"] {
		t.Errorf("expected both plugins to be listed, got %v", names)
	}
}

func TestDefault(t *testing.T) {
	d := Default()
	if d != "claude-code" {
		t.Errorf("expected default 'claude-code', got %q", d)
	}
}
