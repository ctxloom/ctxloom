package config

import (
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
		mcp: wire.MCPConfig{
			Servers: map[string]wire.MCPServer{
				"fs": {Command: "fs-server", Args: []string{"--root", "."}},
			},
			Plugins: map[string]map[string]wire.MCPServer{
				"claude-code": {"fs": {Command: "fs-server"}},
			},
		},
		lm: LMConfig{
			Configs: map[string]LLMConfig{
				"main": {Type: "claude-code", Body: map[string]any{"model": "opus"}},
			},
		},
		profiles: ProfilesConfig{
			Definitions: map[string]Profile{
				"go": {Description: "go work", Tags: []string{"lang"}},
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

	t.Run("MCPServers map and nested slice", func(t *testing.T) {
		got := cfg.GetMCPServers()
		delete(got, "fs")
		if _, ok := cfg.mcp.Servers["fs"]; !ok {
			t.Fatal("deleting from GetMCPServers() result deleted the source entry")
		}
		got2 := cfg.GetMCPServers()
		srv := got2["fs"]
		srv.Args[0] = "MUTATED"
		if cfg.mcp.Servers["fs"].Args[0] != "--root" {
			t.Fatalf("mutating a nested slice on GetMCPServers() result corrupted the source: %v", cfg.mcp.Servers["fs"].Args)
		}
	})

	t.Run("MCPPlugins map of maps", func(t *testing.T) {
		got := cfg.GetMCPPlugins()
		delete(got["claude-code"], "fs")
		if _, ok := cfg.mcp.Plugins["claude-code"]["fs"]; !ok {
			t.Fatal("deleting from a nested map in GetMCPPlugins() result deleted the source entry")
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

	t.Run("ProfileDefinitions map and nested slice", func(t *testing.T) {
		got := cfg.GetProfileDefinitions()
		p := got["go"]
		p.Tags[0] = "MUTATED"
		delete(got, "go")
		if _, ok := cfg.profiles.Definitions["go"]; !ok {
			t.Fatal("deleting from GetProfileDefinitions() result deleted the source entry")
		}
		if cfg.profiles.Definitions["go"].Tags[0] != "lang" {
			t.Fatalf("mutating a nested slice on GetProfileDefinitions() result corrupted the source: %v", cfg.profiles.Definitions["go"].Tags)
		}
	})

	t.Run("ProfilesConfig wrapper map", func(t *testing.T) {
		got := cfg.GetProfilesConfig()
		delete(got.Definitions, "go")
		if _, ok := cfg.profiles.Definitions["go"]; !ok {
			t.Fatal("deleting from GetProfilesConfig().Definitions result deleted the source entry")
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
