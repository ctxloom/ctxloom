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

	existing, err := readExistingConfig(fs, configPath)
	if err != nil {
		return err
	}

	c.applyConfigSections(existing)

	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := afero.WriteFile(fs, configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// readExistingConfig loads the current config file into a generic map so that
// unknown fields are preserved across a Save. A missing file yields an empty
// map; a corrupt one warns and continues (fault tolerance — don't block).
func readExistingConfig(fs afero.Fs, configPath string) (map[string]interface{}, error) {
	existingData, err := afero.ReadFile(fs, configPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read existing config: %w", err)
	}
	existing := make(map[string]interface{})
	if len(existingData) > 0 {
		if err := yaml.Unmarshal(existingData, &existing); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: existing config may be corrupted, unknown fields may be lost: %v\n", err)
		}
	}
	return existing, nil
}

// setOrDelete writes value under key when present, otherwise removes key — so
// emptied sections are pruned from the on-disk config rather than left behind.
func setOrDelete(m map[string]interface{}, key string, present bool, value interface{}) {
	if present {
		m[key] = value
	} else {
		delete(m, key)
	}
}

// applyConfigSections updates existing with the current config values, pruning
// keys for empty sections. Editor round-trips like every other block; without
// it editor settings would be silently dropped.
func (c *Config) applyConfigSections(existing map[string]interface{}) {
	existing["llm"] = c.LM
	delete(existing, "lm")         // remove old key if present
	delete(existing, "generators") // no longer supported

	setOrDelete(existing, "defaults", c.Defaults.hasAny(), c.Defaults)
	setOrDelete(existing, "editor", c.Editor.Command != "" || len(c.Editor.Args) > 0, c.Editor)
	setOrDelete(existing, "profiles", len(c.Profiles) > 0, c.Profiles)
	setOrDelete(existing, "sync", c.Sync.AutoSync != nil || c.Sync.Lock != nil || c.Sync.ApplyHooks != nil, c.Sync)
	setOrDelete(existing, "mcp", len(c.MCP.Servers) > 0 || len(c.MCP.Plugins) > 0 || c.MCP.AutoRegisterCtxloom != nil, c.MCP)
	setOrDelete(existing, "hooks", c.Hooks.hasAny(), c.Hooks)
}
