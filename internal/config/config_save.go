package config

import (
	"fmt"
	"os"
	"reflect"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// CommitUpgrade persists a pending in-memory schema upgrade to disk, writing the
// upgraded bytes verbatim so the comments and key order preserved by the node
// rewrite survive. It is a no-op when nothing is pending, and clears
// PendingUpgrade on success. Callers prompt the user before invoking this (see
// cmd/run.go); ctxloom never rewrites a config without consent.
func (c *Config) CommitUpgrade() error {
	if c.PendingUpgrade == nil {
		return nil
	}
	p := c.PendingUpgrade
	if err := iox.WriteFileAtomicFs(c.getFS(), p.Path, p.Data, 0o644); err != nil {
		return fmt.Errorf("write upgraded config %s: %w", p.Path, err)
	}
	c.PendingUpgrade = nil
	return nil
}

// Save writes the configuration to the primary config file. The
// read-modify-write is serialized across processes with an advisory file lock
// (the MCP server and a concurrent CLI both call Save — without it one
// writer's sections are silently lost), and the write itself is atomic so a
// crash can never tear config.yaml.
func (c *Config) Save() error {
	configPath, err := c.GetConfigFilePath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	fs := c.getFS()

	// Advisory flock applies only to the real filesystem; an injected (test)
	// fs has no cross-process readers. A lock failure degrades to an unlocked
	// save rather than blocking the write (CLAUDE.md fault tolerance).
	if c.fs == nil {
		if unlock, lerr := filelock.Lock(configPath + ".lock"); lerr == nil {
			defer unlock()
		} else {
			clidiag.Warn("ctxloom", "config lock failed, saving unlocked: %v", lerr)
		}
	}

	existing, err := readExistingConfig(fs, configPath)
	if err != nil {
		return err
	}

	c.applyConfigSections(existing)

	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := iox.WriteFileAtomicFs(fs, configPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// Marshal renders the configuration to YAML bytes using the same section
// assembly as Save (registry role-stripping included), but over a fresh map
// rather than the on-disk file. Used by callers that build a config in memory
// and write it themselves (e.g. init), so the written shape matches Save's.
func (c *Config) Marshal() ([]byte, error) {
	out := make(map[string]interface{})
	c.applyConfigSections(out)
	data, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}
	return data, nil
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
			clidiag.Warn("ctxloom", "existing config may be corrupted, unknown fields may be lost: %v", err)
		}
	}
	return existing, nil
}

// userAuthoredLM returns the LM section with default-overlaid values stripped:
// registry entries and role defaults that came verbatim from the embedded
// default config (mergeDefaultConfig) are runtime fallbacks, not user
// configuration. Persisting them would pin the user to a snapshot of shipped
// model defaults that stops tracking future releases. Anything the user added
// or changed since the overlay survives.
func (c *Config) userAuthoredLM() LMConfig {
	lm := c.LM
	ov := c.lmDefaultOverlay
	if ov == nil {
		return lm
	}
	configs := make(map[string]LLMConfig, len(lm.Configs))
	for label, entry := range lm.Configs {
		if def, ok := ov.Configs[label]; ok && reflect.DeepEqual(entry, def) {
			continue
		}
		configs[label] = entry
	}
	lm.Configs = configs
	if ov.Defaults.Primary != "" && lm.Defaults.Primary == ov.Defaults.Primary {
		lm.Defaults.Primary = ""
	}
	if ov.Defaults.Fast != "" && lm.Defaults.Fast == ov.Defaults.Fast {
		lm.Defaults.Fast = ""
	}
	return lm
}

// persistableLM returns a copy of the LM config with the registry-only Role
// dropped from every entry, so persisted user configs carry plain {type, model}
// entries. The input is not mutated (the in-memory registry keeps its roles).
func persistableLM(lm LMConfig) LMConfig {
	if len(lm.Configs) == 0 {
		return lm
	}
	configs := make(map[string]LLMConfig, len(lm.Configs))
	for label, entry := range lm.Configs {
		entry.Role = ""
		configs[label] = entry
	}
	lm.Configs = configs
	return lm
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
	existing["version"] = CurrentConfigVersion // stamp current schema version so saved configs are never stale
	lm := c.userAuthoredLM()
	setOrDelete(existing, "llm", lm.hasAny(), persistableLM(lm))
	delete(existing, "lm")         // remove old key if present
	delete(existing, "generators") // no longer supported

	setOrDelete(existing, "config", c.Settings.hasAny(), c.Settings)
	setOrDelete(existing, "editor", c.Editor.Command != "" || len(c.Editor.Args) > 0, c.Editor)
	setOrDelete(existing, "profiles", c.Profiles.hasAny(), c.Profiles)
	delete(existing, "defaults") // superseded by config + profiles blocks
	// Persist only the config-key subagents (c.Subagents). Directory-sourced
	// subagents live in their own .ctxloom/subagents/*.yaml files and are not
	// folded back into config.yaml. Pruned when empty so an emptied map removes
	// the block rather than leaving `subagents: {}` behind.
	setOrDelete(existing, "subagents", len(c.Subagents) > 0, c.Subagents)
	// Top-level isolation default; pruned when empty ("none" is the implicit
	// default, so an unset policy leaves no key behind).
	setOrDelete(existing, "isolation", c.Isolation != "", c.Isolation)
	setOrDelete(existing, "sync", c.Sync.AutoSync != nil, c.Sync)
	setOrDelete(existing, "mcp", len(c.MCP.Servers) > 0 || len(c.MCP.Plugins) > 0 || c.MCP.AutoRegisterCtxloom != nil, c.MCP)
	setOrDelete(existing, "hooks", c.Hooks.HasAny(), c.Hooks)
}
