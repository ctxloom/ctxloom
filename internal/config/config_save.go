package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"sort"

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
	existingData, existing, err := readExistingConfig(fs, configPath)
	if err != nil {
		return err
	}

	c.applyConfigSections(existing)

	// Normalize the desired sections (applyConfigSections' typed-struct blocks —
	// c.editor, c.settings, ...) into a nested generic map, exactly like a
	// freshly-read config layer, so (a) the layer-scope walker can traverse it via
	// kmaps and (b) each section compares canonically against the on-disk node in
	// marshalPreservingComments below.
	desiredBytes, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	var desired map[string]any
	if err := yaml.Unmarshal(desiredBytes, &desired); err != nil {
		return fmt.Errorf("failed to normalize config for save: %w", err)
	}

	// c is the FULLY MERGED view Manager.Update's loadUncached produced (home <
	// project < env < flag), so applyConfigSections wrote every section it
	// carries regardless of which layer contributed it — a Machine-scoped value
	// set ONLY in home (editor.command, llm.configs.*.env, ...) included. Writing
	// that into configPath is exactly the leak internal/config/layerscope closes:
	// the file being written IS the project layer whenever a separate home layer
	// also exists (c.source == SourceProject), and Scope.Allows(LayerProject)
	// forbids a Machine-scoped value there. Drop each via the SAME
	// dropLayerScopeViolations load-time uses (never a bespoke filter), zap-logged
	// because there is no live *Config.warnings slice to append to here. When
	// c.source is SourceHome (this file IS home acting alone), nothing to filter.
	if c.source == SourceProject {
		for _, v := range dropLayerScopeViolations(layerscope.LayerProject, desired) {
			zap.L().Warn("config_layer_scope_save_warning", zap.Strings("key", v.Path))
		}
	}

	// Persist by PATCHING the on-disk document's yaml.Node tree so comments and
	// key order survive — only a section whose canonical content actually changed
	// is re-encoded, exactly like the comment-preserving upgrade path, rather than
	// re-emitting a sorted, comment-stripped map[string]interface{} marshal on
	// every write (U049-F16). A first write (no existing bytes) falls back to a
	// fresh document whose keys are emitted sorted, matching what Marshal produces.
	data, err := marshalPreservingComments(existingData, desired)
	if err != nil {
		return fmt.Errorf("failed to write config: %w", err)
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

// marshalPreservingComments renders desired to YAML by patching the parsed node
// tree of original, so every comment and the authored key order in original
// survive the write. A key whose canonical value is unchanged keeps its exact
// on-disk node (comments and all); a changed section is re-encoded from desired;
// a key desired no longer carries is dropped; a new key is appended. With no
// original bytes it emits a fresh document (keys sorted, matching a map marshal).
func marshalPreservingComments(original []byte, desired map[string]any) ([]byte, error) {
	var doc yaml.Node
	haveDoc := false
	if len(original) > 0 {
		if err := yaml.Unmarshal(original, &doc); err != nil {
			// readExistingConfig already parse-checked; treat a re-parse failure as
			// a hard error rather than silently truncating.
			return nil, fmt.Errorf("re-parse existing config for patch: %w", err)
		}
		if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
			haveDoc = true
		}
	}
	var root *yaml.Node
	if haveDoc {
		root = doc.Content[0]
	} else {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if err := reconcileMappingNode(root, desired); err != nil {
		return nil, err
	}
	if haveDoc {
		return yaml.Marshal(&doc)
	}
	return yaml.Marshal(root)
}

// reconcileMappingNode mutates root (a mapping node) so it represents desired,
// touching a key only when its content changed so untouched keys keep their
// authored comments and position.
func reconcileMappingNode(root *yaml.Node, desired map[string]any) error {
	// Drop keys the desired state no longer carries (pruned/retired sections),
	// leaving every surviving key node — and its comments — in place.
	for i := 0; i+1 < len(root.Content); {
		if _, ok := desired[root.Content[i].Value]; ok {
			i += 2
			continue
		}
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
	}
	// Set or replace each desired key, but re-encode only a section whose content
	// actually changed. New keys are appended in a stable (sorted) order.
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		want := desired[key]
		if cur := upgrade.MapValue(root, key); cur != nil {
			same, err := nodeCanonicallyEqual(cur, want)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		var enc yaml.Node
		if err := enc.Encode(want); err != nil {
			return fmt.Errorf("encode config section %q: %w", key, err)
		}
		upgrade.MapSet(root, key, &enc)
	}
	return nil
}

// nodeCanonicallyEqual reports whether an on-disk node and a desired value carry
// the same content, ignoring comments and key order — the test for "this section
// did not change, so keep its authored node". Both sides are normalized through
// a decode→re-marshal so a struct's field order and a map's sorted order compare
// equal.
func nodeCanonicallyEqual(node *yaml.Node, v any) (bool, error) {
	a, err := canonicalYAML(node)
	if err != nil {
		return false, err
	}
	b, err := canonicalYAML(v)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}

func canonicalYAML(v any) ([]byte, error) {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var g any
	if err := yaml.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	return yaml.Marshal(g)
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
func readExistingConfig(fs afero.Fs, configPath string) ([]byte, map[string]interface{}, error) {
	existingData, err := afero.ReadFile(fs, configPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("failed to read existing config: %w", err)
	}
	existing := make(map[string]interface{})
	if len(existingData) > 0 {
		if err := yaml.Unmarshal(existingData, &existing); err != nil {
			return nil, nil, fmt.Errorf("refusing to write over %s: it does not parse as YAML, and saving would replace it with a truncated file — fix or move it first: %w", configPath, err)
		}
	}
	return existingData, existing, nil
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
	// Pruned when empty so an emptied map removes the block rather than
	// leaving `agents: {}` behind.
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
	// Agent delegation's settings (concurrency resource ceiling + depth
	// structural ceiling + the spool shadow tee — see DelegationConfig's doc);
	// pruned as a whole key when none is set (<=0 / false means "use the
	// built-in default"). Wired here so a save/Marshal() round-trip does not
	// silently drop it (the exact bug class dirty_tree_handler's own comment
	// above documents having hit) — and EVERY field of the group has to be in
	// this condition, because a group pruned on a stale subset of its own
	// fields discards the ones nobody remembered to add.
	// Renamed/regrouped from the flat agent_turn_cap — see
	// errRetiredAgentTurnCapKey.
	setOrDelete(existing, "delegation", c.delegation.Concurrency > 0 || c.delegation.Depth > 0 || c.delegation.SpoolTee || c.delegation.SpoolDelivery, c.delegation)
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
	setOrDelete(existing, "hooks", c.hooks.HasAny(), c.hooks)
}
