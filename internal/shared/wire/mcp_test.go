package wire

import "testing"

func TestMCPConfig_ShouldAutoRegisterCtxloom(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name string
		cfg  *MCPConfig
		want bool
	}{
		{"nil config", nil, true},
		{"nil value defaults true", &MCPConfig{}, true},
		{"explicit true", &MCPConfig{AutoRegisterCtxloom: &trueVal}, true},
		{"explicit false", &MCPConfig{AutoRegisterCtxloom: &falseVal}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ShouldAutoRegisterCtxloom(); got != tt.want {
				t.Errorf("ShouldAutoRegisterCtxloom() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeMCPConfig_NilInputs(t *testing.T) {
	t.Run("nil dest does nothing", func(t *testing.T) {
		src := &MCPConfig{Servers: map[string]MCPServer{"test": {Command: "test-cmd"}}}
		MergeMCPConfig(nil, src) // must not panic
	})

	t.Run("nil src leaves dest untouched", func(t *testing.T) {
		dest := &MCPConfig{}
		MergeMCPConfig(dest, nil)
		if dest.Servers != nil {
			t.Errorf("Servers = %v, want nil", dest.Servers)
		}
	})

	t.Run("both nil does nothing", func(t *testing.T) {
		MergeMCPConfig(nil, nil) // must not panic
	})
}

func TestMergeMCPConfig_AutoRegisterCtxloom(t *testing.T) {
	t.Run("src overrides dest", func(t *testing.T) {
		trueVal, falseVal := true, false
		dest := &MCPConfig{AutoRegisterCtxloom: &trueVal}
		src := &MCPConfig{AutoRegisterCtxloom: &falseVal}
		MergeMCPConfig(dest, src)
		if *dest.AutoRegisterCtxloom {
			t.Error("AutoRegisterCtxloom = true, want false")
		}
	})

	t.Run("nil src value preserves dest", func(t *testing.T) {
		trueVal := true
		dest := &MCPConfig{AutoRegisterCtxloom: &trueVal}
		MergeMCPConfig(dest, &MCPConfig{})
		if !*dest.AutoRegisterCtxloom {
			t.Error("AutoRegisterCtxloom = false, want true")
		}
	})
}

func TestMergeMCPConfig_UnifiedServers(t *testing.T) {
	t.Run("creates servers map if nil", func(t *testing.T) {
		dest := &MCPConfig{}
		src := &MCPConfig{Servers: map[string]MCPServer{
			"test-server": {Command: "test-cmd", Args: []string{"arg1"}},
		}}
		MergeMCPConfig(dest, src)
		if dest.Servers == nil {
			t.Fatal("Servers is nil, want populated")
		}
		if got := dest.Servers["test-server"].Command; got != "test-cmd" {
			t.Errorf("Command = %q, want %q", got, "test-cmd")
		}
	})

	t.Run("src overrides dest for same name", func(t *testing.T) {
		dest := &MCPConfig{Servers: map[string]MCPServer{"server": {Command: "old-cmd"}}}
		src := &MCPConfig{Servers: map[string]MCPServer{"server": {Command: "new-cmd"}}}
		MergeMCPConfig(dest, src)
		if got := dest.Servers["server"].Command; got != "new-cmd" {
			t.Errorf("Command = %q, want %q", got, "new-cmd")
		}
	})
}

func TestMergeMCPConfig_PluginSpecificServers(t *testing.T) {
	t.Run("creates plugin map if nil", func(t *testing.T) {
		dest := &MCPConfig{}
		src := &MCPConfig{Plugins: map[string]map[string]MCPServer{
			"claude-code": {"my-server": {Command: "my-cmd"}},
		}}
		MergeMCPConfig(dest, src)
		if dest.Plugins == nil {
			t.Fatal("Plugins is nil, want populated")
		}
		if got := dest.Plugins["claude-code"]["my-server"].Command; got != "my-cmd" {
			t.Errorf("Command = %q, want %q", got, "my-cmd")
		}
	})

	t.Run("merges multiple backends", func(t *testing.T) {
		dest := &MCPConfig{Plugins: map[string]map[string]MCPServer{
			"claude-code": {"existing": {Command: "existing-cmd"}},
		}}
		src := &MCPConfig{Plugins: map[string]map[string]MCPServer{
			"claude-code": {"new": {Command: "new-cmd"}},
			"antigravity": {"antigravity-server": {Command: "antigravity-cmd"}},
		}}
		MergeMCPConfig(dest, src)
		for _, c := range []struct{ backend, server, want string }{
			{"claude-code", "existing", "existing-cmd"},
			{"claude-code", "new", "new-cmd"},
			{"antigravity", "antigravity-server", "antigravity-cmd"},
		} {
			if got := dest.Plugins[c.backend][c.server].Command; got != c.want {
				t.Errorf("Plugins[%q][%q].Command = %q, want %q", c.backend, c.server, got, c.want)
			}
		}
	})
}
