package config

import (
	"fmt"
	"os"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// ConfigFile represents the structure for saving config.yaml
type ConfigFile struct {
	LM       LMConfig           `yaml:"llm"`
	Editor   EditorConfig       `yaml:"editor,omitempty"`
	Defaults Defaults           `yaml:"defaults,omitempty"`
	Sync     SyncConfig         `yaml:"sync,omitempty"`
	Hooks    HooksConfig        `yaml:"hooks,omitempty"`
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
}

// Save writes the configuration to the primary config file.
func (c *Config) Save() error {
	configPath, err := c.GetConfigFilePath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	fs := c.getFS()

	// Read existing config to preserve unknown fields
	existingData, err := afero.ReadFile(fs, configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read existing config: %w", err)
	}
	existing := make(map[string]interface{})
	if len(existingData) > 0 {
		if err := yaml.Unmarshal(existingData, &existing); err != nil {
			// Warn but continue - fault tolerance principle: don't block operations
			fmt.Fprintf(os.Stderr, "ctxloom: warning: existing config may be corrupted, unknown fields may be lost: %v\n", err)
		}
	}

	// Update with current values (delete keys when empty to clean up config)
	existing["llm"] = c.LM
	delete(existing, "lm") // Remove old key if present

	if c.Defaults.hasAny() {
		existing["defaults"] = c.Defaults
	} else {
		delete(existing, "defaults")
	}

	// Editor settings round-trip through Save like every other block; without
	// this they'd be silently dropped (the ConfigFile struct declares an
	// `editor` field but Save never wrote it).
	if c.Editor.Command != "" || len(c.Editor.Args) > 0 {
		existing["editor"] = c.Editor
	} else {
		delete(existing, "editor")
	}

	if len(c.Profiles) > 0 {
		existing["profiles"] = c.Profiles
	} else {
		delete(existing, "profiles")
	}

	// Save sync config if any values are set
	if c.Sync.AutoSync != nil || c.Sync.Lock != nil || c.Sync.ApplyHooks != nil {
		existing["sync"] = c.Sync
	} else {
		delete(existing, "sync")
	}

	// Remove generators key if present (no longer supported)
	delete(existing, "generators")

	if len(c.MCP.Servers) > 0 || len(c.MCP.Plugins) > 0 || c.MCP.AutoRegisterCtxloom != nil {
		existing["mcp"] = c.MCP
	} else {
		delete(existing, "mcp")
	}

	if c.Hooks.hasAny() {
		existing["hooks"] = c.Hooks
	} else {
		delete(existing, "hooks")
	}

	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := afero.WriteFile(fs, configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}
