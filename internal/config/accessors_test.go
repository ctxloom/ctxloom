package config

import (
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestAccessor_ReturnedMapIsNotTheInternalOne asserts every map/slice
// accessor added in accessors.go hands back a fresh container: mutating (or
// deleting from) the returned value must never be observable through the
// Config it came from. This is the copy-on-read guarantee accessors.go's
// package doc commits to — without it, a getter would just relocate the
// armed-chomp/tall-nanny shared-mutation bug to a new call site.
func TestAccessor_ReturnedMapIsNotTheInternalOne(t *testing.T) {
	cfg := &Config{
		appPaths: []string{"/proj/.ctxloom"},
		warnings: []Warning{{Kind: WarnKindRead, Text: "original"}},
		agents: map[string]agents.Agent{
			"dev": {Name: "dev", Profiles: []string{"go", "review"}},
		},
		lm: LMConfig{
			Configs: map[string]LLMConfig{
				"main": {Type: "claude-code", Body: map[string]any{"model": "opus"}},
			},
		},
		isolationImages: map[string]string{"claude-code": "img:latest"},
	}

	t.Run("AppPaths slice", func(t *testing.T) {
		got := cfg.GetAppPaths()
		got[0] = "MUTATED"
		if cfg.appPaths[0] != "/proj/.ctxloom" {
			t.Fatalf("mutating GetAppPaths() result corrupted the source: %v", cfg.appPaths)
		}
	})

	t.Run("Warnings slice", func(t *testing.T) {
		got := cfg.GetWarnings()
		got[0].Text = "MUTATED"
		if cfg.warnings[0].Text != "original" {
			t.Fatalf("mutating GetWarnings() result corrupted the source: %v", cfg.warnings)
		}
	})

	t.Run("ConfiguredAgents map and nested slice", func(t *testing.T) {
		got := cfg.GetConfiguredAgents()
		delete(got, "dev")
		if _, ok := cfg.agents["dev"]; !ok {
			t.Fatal("deleting from GetConfiguredAgents() result deleted the source entry")
		}
		got2 := cfg.GetConfiguredAgents()
		dev := got2["dev"]
		dev.Profiles[0] = "MUTATED"
		if cfg.agents["dev"].Profiles[0] != "go" {
			t.Fatalf("mutating a nested slice on GetConfiguredAgents() result corrupted the source: %v", cfg.agents["dev"].Profiles)
		}
	})

	t.Run("LMConfig map and nested Body", func(t *testing.T) {
		got := cfg.GetLMConfig()
		got.Configs["main"].Body["model"] = "MUTATED"
		delete(got.Configs, "main")
		if _, ok := cfg.lm.Configs["main"]; !ok {
			t.Fatal("deleting from GetLMConfig().Configs deleted the source entry")
		}
		if cfg.lm.Configs["main"].Body["model"] != "opus" {
			t.Fatalf("mutating GetLMConfig().Configs[...].Body corrupted the source: %v", cfg.lm.Configs["main"].Body)
		}
	})

	t.Run("Settings pointer fields", func(t *testing.T) {
		trueVal := true
		src := &Config{settings: SettingsConfig{UseDistilled: &trueVal}}
		got := src.GetSettings()
		*got.UseDistilled = false
		if !*src.settings.UseDistilled {
			t.Fatal("mutating GetSettings() result's pointer field corrupted the source")
		}
	})
}

// mcpCloneShapeCases are the shapes an MCPServer deep copy has to get right:
// the nil/empty distinction on both containers (yaml `omitempty` renders those
// differently, so flattening one into the other changes persisted bytes) and a
// populated value.
var mcpCloneShapeCases = []struct {
	name string
	in   wire.MCPServer
}{
	{"zero", wire.MCPServer{}},
	{"nil containers", wire.MCPServer{Command: "srv"}},
	{"empty non-nil containers", wire.MCPServer{Command: "srv", Args: []string{}, Env: map[string]string{}}},
	{"populated", wire.MCPServer{
		Command: "srv", Args: []string{"a", "b"},
		Env: map[string]string{"K": "V"}, Notes: "n", Installation: "i", SCM: "m",
	}},
}

// TestCloneMCPServer_PreservesShape pins that the deep copy every MCP accessor
// in this package hands out is field-for-field the value it copied, INCLUDING
// each container's nil-vs-empty state.
func TestCloneMCPServer_PreservesShape(t *testing.T) {
	for _, tc := range mcpCloneShapeCases {
		t.Run(tc.name, func(t *testing.T) {
			got := wire.CloneMCPServer(tc.in)
			if (got.Args == nil) != (tc.in.Args == nil) {
				t.Fatalf("Args nil-ness changed: in nil=%v out nil=%v", tc.in.Args == nil, got.Args == nil)
			}
			if (got.Env == nil) != (tc.in.Env == nil) {
				t.Fatalf("Env nil-ness changed: in nil=%v out nil=%v", tc.in.Env == nil, got.Env == nil)
			}
			if !reflect.DeepEqual(got, tc.in) {
				t.Fatalf("clone differs from source:\n in=%#v\nout=%#v", tc.in, got)
			}
		})
	}
}

// TestCloneMCPServer_NeverAliases pins the reason a deep copy exists at all: a
// plain struct copy shares the Args backing array and the Env map, so a caller
// mutating what it was handed would reach the value it was copied from.
func TestCloneMCPServer_NeverAliases(t *testing.T) {
	in := wire.MCPServer{Command: "srv", Args: []string{"a"}, Env: map[string]string{"K": "V"}}
	got := wire.CloneMCPServer(in)
	got.Args[0] = "mutated"
	got.Env["K"] = "mutated"
	if in.Args[0] != "a" || in.Env["K"] != "V" {
		t.Fatalf("clone aliases its source: args=%v env=%v", in.Args, in.Env)
	}
}
