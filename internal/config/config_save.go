package config

import (
	"fmt"
	"os"
	"reflect"

	"github.com/spf13/afero"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config/layerscope"
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
	// Nothing to write is not a successful write. The caller has just asked the
	// user to consent to a REWRITE, so returning nil says that rewrite landed —
	// while an empty payload lands as a zero-byte config.yaml over a file that
	// was valid until this moment.
	if len(p.Data) == 0 {
		return fmt.Errorf("pending upgrade for %s carries no content; refusing to truncate it", p.Path)
	}
	if err := iox.WriteFileAtomicFs(c.getFS(), p.Path, p.Data, 0o644); err != nil {
		return fmt.Errorf("write upgraded config %s: %w", p.Path, err)
	}
	return nil
}

// saveLocked is the read-merge-write at the heart of persisting a Config: it
// re-reads the on-disk file fresh, merges c's in-memory sections onto it
// (preserving unknown keys), and writes back atomically so a crash can never
// tear config.yaml. It takes no lock of its own — the caller (Manager.Update,
// the only production writer) is responsible for holding the advisory
// cross-process file lock for the whole read-modify-write, which is what
// actually closes the lost-update window: two writers that each captured
// their own in-memory Config before the lock was ever taken would otherwise
// silently discard one another's change, despite the write itself never
// interleaving at the byte level.
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

	// c is the FULLY MERGED view Manager.Update's loadUncached produced (home
	// < project < env < flag), so applyConfigSections just wrote every
	// section it carries into existing regardless of which layer originally
	// contributed it — a Machine-scoped value set ONLY in home (editor.
	// command, llm.configs.*.env, ...) included. Writing that into configPath
	// is exactly the leak internal/config/layerscope exists to close: the
	// file being written here IS the project layer whenever a separate home
	// layer also exists (c.source == SourceProject — see
	// resolveConfigLayerPaths), and Scope.Allows(LayerProject) says a
	// Machine-scoped value may not live there, committed-file-visible to
	// every clone, no matter which in-memory Config section carried it in.
	// Left unfiltered, the NEXT load of this same file re-discovers the
	// leaked value at LayerProject and (in FATAL-class strictness) refuses
	// to start — a command with nothing to do with editor.command (e.g. `mcp
	// server create`) would otherwise brick every subsequent strict-mode
	// invocation. When c.source is SourceHome (no project directory exists
	// at all; this file IS home acting alone, nothing to violate), the
	// filter is skipped.
	if c.source == SourceProject {
		filtered, ferr := dropSaveLayerScopeViolations(data)
		if ferr != nil {
			return fmt.Errorf("failed to enforce layer scope on config: %w", ferr)
		}
		data = filtered
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

// dropSaveLayerScopeViolations re-decodes marshaled config YAML into a
// generic map[string]any — undoing applyConfigSections' typed-struct
// sections (c.editor, c.mcp, c.settings, ...) so kmaps can walk the result
// uniformly, exactly like a freshly-read config layer — and drops whatever
// ScopeProject disallows via the SAME dropLayerScopeViolations
// load-time uses (never a second, bespoke filter), before re-marshaling.
// Each drop is zap-logged (config_layer_scope_save_warning): there is no
// live *Config.warnings slice to append to at this point in a save (the
// fresh Config Manager.Update built is local to that call and about to be
// discarded), so a zap log is the only channel available — silently
// dropping it with no trace at all would repeat exactly the failure mode
// WarnKindLayerScope's own load-time doc rejects.
func dropSaveLayerScopeViolations(data []byte) ([]byte, error) {
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, fmt.Errorf("re-parse marshaled config: %w", err)
	}
	for _, v := range dropLayerScopeViolations(layerscope.LayerProject, generic) {
		zap.L().Warn("config_layer_scope_save_warning", zap.Strings("key", v.Path))
	}
	out, err := yaml.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("re-marshal filtered config: %w", err)
	}
	return out, nil
}

// Marshal renders the configuration to YAML bytes using the same section
// assembly as saveLocked (registry role-stripping included), but over a fresh
// map rather than the on-disk file. Used by callers that build a config in
// memory and write it themselves (e.g. init), so the written shape matches
// what a save produces.
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
// unknown fields are preserved across a save. A missing file yields an empty
// map — that is the normal first-write shape.
//
// A file that will not PARSE is refused. The old behaviour warned and
// returned an empty map, and saveLocked then atomically replaced the file
// with only the sections applyConfigSections emits: every key ctxloom does
// not model, and every key it does model but this in-memory Config happens
// not to carry, was destroyed by a command the user ran for an unrelated
// reason (`ctxloom agent add`, `mcp add`, anything through Manager.Update).
// The warning even said so — "unknown fields may be lost" — while proceeding
// to lose them.
//
// "I could not read what is there" is not "there is nothing there", and it is
// not a licence to overwrite it. A corrupt config is a reason to stop: the
// user still has their file and can fix the one line that broke it, which is
// impossible once we have rewritten it.
func readExistingConfig(fs afero.Fs, configPath string) (map[string]interface{}, error) {
	existingData, err := afero.ReadFile(fs, configPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read existing config: %w", err)
	}
	existing := make(map[string]interface{})
	if len(existingData) > 0 {
		if err := yaml.Unmarshal(existingData, &existing); err != nil {
			return nil, fmt.Errorf("refusing to write over %s: it does not parse as YAML, and saving would replace it with a truncated file — fix or move it first: %w", configPath, err)
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
	// dirty_tree_handler default; pruned when empty so an unset project falls
	// through to the built-in default ("commit", unacknowledged — the
	// "commit" handler still refuses). This was declared on configDoc/Fixture
	// (toDoc/fromDoc, ToFixture/NewFixture) when the dirty-tree handler
	// feature landed but never wired into this section-by-section persist
	// path, so ANY caller that set it via a save or Marshal() (rather than a
	// raw yaml.Marshal(cfg)) silently lost it — exactly this project's
	// characteristic bug. Fixed here so the init-interview write
	// (internal/cli/init.go's promptDirtyTreeHandler) actually lands it on
	// disk. Its commit acknowledgement is no longer a config key at all — see
	// DirtyTreeCommitAcknowledged/SetDirtyTreeCommitAck.
	setOrDelete(existing, "dirty_tree_handler", c.dirtyTreeHandler != "", c.dirtyTreeHandler)
	setOrDelete(existing, "runtime", c.runtime != "", c.runtime)
	// The project-wide default permission posture; pruned when empty so an
	// undeclared project falls through to the engine's own built-in default
	// rather than persisting a posture nobody chose. Written here for the same
	// reason dirty_tree_handler above documents: a field declared on
	// configDoc/Fixture but missing from this section-by-section persist path is
	// silently discarded on every Save()/Marshal().
	setOrDelete(existing, "permissions", c.permissions != "", c.permissions)
	// Agent delegation's two limits (concurrency resource ceiling + depth
	// structural ceiling — see DelegationConfig's doc); pruned as a whole key
	// when neither is set (<=0 means "use the built-in default"). Wired here
	// so a save/Marshal() round-trip does not silently drop it (the exact bug
	// class dirty_tree_handler's own comment above documents having hit).
	// Renamed/regrouped from the flat agent_turn_cap — see
	// errRetiredAgentTurnCapKey.
	setOrDelete(existing, "delegation", c.delegation.Concurrency > 0 || c.delegation.Depth > 0, c.delegation)
	// Per-backend user-provided agent images; pruned when empty (built-in
	// defaults leave no key behind).
	setOrDelete(existing, "isolation_images", len(c.isolationImages) > 0, c.isolationImages)
	setOrDelete(existing, "isolation_base_containerfile", c.isolationBaseContainerfile != "", c.isolationBaseContainerfile)
	// Agent-observation viewer settings (prefix key + surround bar). Declared
	// on configDoc/Fixture when the viewer landed but never wired here — the
	// THIRD instance of the bug dirty_tree_handler and agent_turn_cap above
	// each document. Pruned when neither field is set; Surround is a *bool
	// precisely so an explicit `surround: false` is distinguishable from
	// unset and survives the round trip.
	setOrDelete(existing, "ui", c.ui.PrefixKey != "" || c.ui.Surround != nil, c.ui)
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
