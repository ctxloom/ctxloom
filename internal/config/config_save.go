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
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// CommitUpgrade persists a pending in-memory schema upgrade to disk, writing the
// upgraded bytes verbatim so the comments and key order preserved by the node
// rewrite survive. It is a no-op when nothing is pending, and clears
// PendingUpgrade on success. Callers prompt the user before invoking this (see
// cmd/run.go); ctxloom never rewrites a config without consent.
func (c *Config) CommitUpgrade() error {
	if err := c.commitPendingUpgrade(c.pendingUpgrade); err != nil {
		return err
	}
	c.pendingUpgrade = nil
	return nil
}

// CommitHomeUpgrade is CommitUpgrade for the HOME layer (HomePendingUpgrade),
// used when a project config.yaml also exists and home is therefore read as
// the lower-precedence layer.
//
// Without it, a stale ~/.ctxloom/config.yaml was upgraded in memory on every
// single load and never written back: the home file never converged and the
// upgrade pipeline redid identical work forever (long-ice). The write itself
// needed no new machinery — the shared committer is keyed on Pending.Path, so
// "a file other than the ambient one" was never actually the hard part.
//
// Same consent rule as CommitUpgrade, and it matters more here: the caller
// prompts first (the prompt names the path, so a user sees it is their HOME
// file), and ctxloom never rewrites home as a silent side effect of a
// project-scoped run.
func (c *Config) CommitHomeUpgrade() error {
	if err := c.commitPendingUpgrade(c.homePendingUpgrade); err != nil {
		return err
	}
	c.homePendingUpgrade = nil
	return nil
}

// commitPendingUpgrade writes one pending upgrade's bytes to its own recorded
// path, verbatim so the comments and key order preserved by the node rewrite
// survive. Shared by both layers' committers so they cannot drift on how an
// upgrade is persisted; nil is a no-op.
func (c *Config) commitPendingUpgrade(p *upgrade.Pending) error {
	if p == nil {
		return nil
	}
	if err := iox.WriteFileAtomicFs(c.getFS(), p.Path, p.Data, 0o644); err != nil {
		return fmt.Errorf("write upgraded config %s: %w", p.Path, err)
	}
	return nil
}

// Save writes the configuration to the primary config file. The write itself
// is serialized across processes with an advisory file lock (the MCP server
// and a concurrent CLI both call Save — without it one writer's sections are
// silently lost mid-write) and is atomic so a crash can never tear
// config.yaml.
//
// Save does NOT, by itself, close the LOST-UPDATE window: it re-reads the
// on-disk file fresh under its own lock, but the fields it merges onto that
// fresh read (c's own agents/mcp/... state) may have been populated by a
// Load() long before the lock was ever taken — so two callers that each
// Load(), mutate their own in-memory copy, then Save() can still silently
// discard one another's change, despite neither Save() call ever
// interleaving with the other at the byte level. Manager.Update (see
// config_manager.go) is what actually closes that window: it takes the SAME
// lock this method does, but re-Loads fresh AFTER acquiring it — so the
// in-memory state Save eventually merges is never stale. Save remains the
// entry point for callers that accept that risk (or, like Manager.Update,
// have already closed it themselves via saveLocked).
func (c *Config) Save() error {
	configPath, err := c.GetConfigFilePath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	fs := c.getFS()

	// Advisory flock applies only to the real filesystem; an injected (test)
	// fs has no cross-process readers. A lock failure degrades to an unlocked
	// save rather than blocking the write (CLAUDE.md fault tolerance).
	// Gated on injectedFS, NOT c.fs's nilness — loadUncached always populates
	// c.fs with a concrete value (afero.NewOsFs() by default), so c.fs is
	// never actually nil for a Load()-produced Config; see injectedFS's doc.
	if !c.injectedFS {
		if unlock, lerr := filelock.Lock(configPath + ".lock"); lerr == nil {
			defer unlock()
		} else {
			clidiag.Warn("ctxloom", "config lock failed, saving unlocked: %v", lerr)
		}
	}

	return c.saveLocked(fs, configPath)
}

// saveLocked is Save's actual read-merge-write, factored out so
// Manager.Update can reuse it from INSIDE a lock it already holds — calling
// Save() there would try to re-acquire the same advisory flock from the same
// process and deadlock (flock(2) blocks the calling process until the lock
// is released, and a process can never release a lock it is still waiting to
// acquire a second one of). Callers of saveLocked are responsible for their
// own locking (or for deciding, like Save() and Manager.Update both do, that
// an unavailable lock degrades to an unlocked write rather than blocking
// forever).
func (c *Config) saveLocked(fs afero.Fs, configPath string) error {
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

	// The ambient memo now describes a superseded file. Load's stat check would
	// catch this on its own; dropping the memo here makes the write→read
	// ordering explicit rather than dependent on mtime granularity.
	Invalidate()

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
	lm := c.lm
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

	setOrDelete(existing, "config", c.settings.hasAny(), c.settings)
	setOrDelete(existing, "editor", c.editor.Command != "" || len(c.editor.Args) > 0, c.editor)
	setOrDelete(existing, "profiles", c.profiles.hasAny(), c.profiles)
	delete(existing, "defaults") // superseded by config + profiles blocks
	// Persist only the config-key agents (c.agents). Directory-sourced
	// agents live in their own .ctxloom/agents/*.yaml files and are not
	// folded back into config.yaml. Pruned when empty so an emptied map removes
	// the block rather than leaving `agents: {}` behind.
	setOrDelete(existing, "agents", len(c.agents) > 0, c.agents)
	// The always-bound default agent (replaces the retired profiles.defaults);
	// pruned when empty so an unset default_agent leaves no key behind.
	setOrDelete(existing, "default_agent", c.defaultAgent != "", c.defaultAgent)
	// Session-level workspace default + agent-level runtime default; pruned
	// when empty ("none"/"host" are the implicit defaults, so unset axes
	// leave no keys behind).
	setOrDelete(existing, "workspace", c.workspace != "", c.workspace)
	// dirty_tree_handler default + its human-only commit acknowledgement
	// (config.Config.dirtyTreeCommitAck's doc); pruned when empty/false so an
	// unset project falls through to the built-in default ("commit",
	// unacknowledged — the "commit" handler still refuses). These were
	// declared on configDoc/Fixture (toDoc/fromDoc, ToFixture/NewFixture) when
	// the dirty-tree handler feature landed but never wired into this
	// section-by-section persist path, so ANY caller that set them via Save()
	// or Marshal() (rather than a raw yaml.Marshal(cfg)) silently lost them —
	// exactly this project's characteristic bug. Fixed here so the
	// init-interview write (internal/cli/init.go's promptDirtyTreeHandler)
	// actually lands both keys on disk.
	setOrDelete(existing, "dirty_tree_handler", c.dirtyTreeHandler != "", c.dirtyTreeHandler)
	setOrDelete(existing, "dirty_tree_commit_ack", c.dirtyTreeCommitAck, c.dirtyTreeCommitAck)
	setOrDelete(existing, "runtime", c.runtime != "", c.runtime)
	// Per-backend user-provided agent images; pruned when empty (built-in
	// defaults leave no key behind).
	setOrDelete(existing, "isolation_images", len(c.isolationImages) > 0, c.isolationImages)
	setOrDelete(existing, "isolation_base_containerfile", c.isolationBaseContainerfile != "", c.isolationBaseContainerfile)
	if c.isolationDevcontainerBase != nil {
		setOrDelete(existing, "isolation_devcontainer_base", true, *c.isolationDevcontainerBase)
	} else {
		delete(existing, "isolation_devcontainer_base")
	}
	setOrDelete(existing, "isolation_devcontainer_service", c.isolationDevcontainerService != "", c.isolationDevcontainerService)
	setOrDelete(existing, "isolation_engines", len(c.isolationEngines) > 0, c.isolationEngines)
	setOrDelete(existing, "sync", c.sync.AutoSync != nil, c.sync)
	setOrDelete(existing, "mcp", len(c.mcp.Servers) > 0 || len(c.mcp.Plugins) > 0 || c.mcp.AutoRegisterCtxloom != nil, c.mcp)
	setOrDelete(existing, "hooks", c.hooks.HasAny(), c.hooks)
}
