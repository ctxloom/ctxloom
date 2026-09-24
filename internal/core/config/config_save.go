package config

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/config/layerscope"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
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
func (c *Config) commitPendingUpgrade(p *PendingUpgrade) error {
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
// tear config.yaml. It takes no lock of its own — the caller (Owner.Update,
// the only production writer) is responsible for holding the advisory
// cross-process file lock for the whole read-modify-write, which is what
// actually closes the lost-update window: two writers that each captured
// their own in-memory Config before the lock was ever taken would otherwise
// silently discard one another's change, despite the write itself never
// interleaving at the byte level.
func (c *Config) saveLocked(fs afero.Fs, configPath string) error {
	existingData, merged, err := readExistingConfig(fs, configPath)
	if err != nil {
		return err
	}

	// The persisted document owns every key configDoc declares: each is
	// replaced by what persistedDoc renders, or removed when that rendering
	// prunes it. Keys ctxloom does not model are left exactly as the file has
	// them; retired keys are dropped.
	desired, err := c.persistedDoc().yamlMap()
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	for _, key := range retiredConfigKeys {
		delete(merged, key)
	}
	for _, key := range persistedKeys() {
		delete(merged, key)
	}
	maps.Copy(merged, desired)

	// c is the FULLY MERGED view Owner.Update's fresh Read produced (home <
	// project < env < flag), so persistedDoc carries every section regardless
	// of which layer contributed it — a Machine-scoped value set ONLY in home
	// (editor.command, llm.configs.*.binary_path, ...) included. Writing that
	// into configPath is exactly the leak internal/core/config/layerscope
	// closes: the file being written IS the project layer whenever a separate
	// home layer also exists (c.source == SourceProject), and
	// Scope.Allows(LayerProject) forbids a Machine-scoped value there. The
	// filter runs over the WHOLE merged file, not just the modeled sections,
	// because the policy also covers keys configDoc does not model
	// (mcp.servers.*.env). Drop each via the SAME dropLayerScopeViolations
	// load-time uses (never a bespoke filter), zap-logged because there is no
	// live *Config.warnings slice to append to here. When c.source is
	// SourceHome (this file IS home acting alone), nothing to filter.
	if c.source == SourceProject {
		for _, v := range DropLayerScopeViolations(layerscope.LayerProject, merged) {
			zap.L().Warn("config_layer_scope_save_warning", zap.Strings("key", v.Path))
		}
	}

	// Persist by PATCHING the on-disk document's yaml.Node tree so comments and
	// key order survive — only a section whose canonical content actually changed
	// is re-encoded, exactly like the comment-preserving upgrade path, rather than
	// re-emitting a sorted, comment-stripped map[string]interface{} marshal on
	// every write (U049-F16). A first write (no existing bytes) emits a fresh
	// document with every key sorted: the same bytes yaml.Marshal(c) produces.
	data, err := marshalPreservingComments(existingData, merged)
	if err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if err := iox.WriteFileAtomicFs(fs, configPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

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
		if cur := mappingValue(root, key); cur != nil {
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
		yamlx.MapSet(root, key, &enc)
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

// readExistingConfig loads the current config file into a generic map so that
// unknown fields are preserved across a save. A missing file yields an empty
// map — that is the normal first-write shape.
//
// A file that will not PARSE is refused. The old behaviour warned and
// returned an empty map, and saveLocked then atomically replaced the file
// with only the sections persistedDoc renders: every key ctxloom does
// not model, and every key it does model but this in-memory Config happens
// not to carry, was destroyed by a command the user ran for an unrelated
// reason (`ctxloom agent add`, `mcp add`, anything through Owner.Update).
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

// persistedDoc is the ONE rendering policy for a Config: `config show`, init's
// scaffold write and every save all emit this document. It is toDoc's
// lossless copy with three rules applied — the version is stamped current, so
// a written config is never stale; shipped default registry entries are
// dropped (userAuthoredLM); and the registry-only Role is stripped
// (persistableLM). Key order is configDoc.MarshalYAML's. toDoc itself stays
// lossless because Owner.Update and ToFixture hand it out as a copy of the
// Config, not as its rendering.
func (c *Config) persistedDoc() configDoc {
	d := c.toDoc()
	d.Version = CurrentConfigVersion
	d.LM = persistableLM(userAuthoredLM(d.LM, c.lmDefaultOverlay))
	return d
}

// retiredConfigKeys are top-level keys ctxloom once wrote and no longer
// models; a save removes them from the file rather than carrying them forward
// as unknown keys.
var retiredConfigKeys = []string{
	"lm",         // renamed to llm
	"generators", // no longer supported
	"profiles",   // the inline arm is retired; profiles are files
	"defaults",   // superseded by the config block
}

// persistedKeys is every top-level key configDoc declares, read off its yaml
// tags so it cannot fall behind the document it describes.
func persistedKeys() []string {
	typ := reflect.TypeOf(configDoc{})
	keys := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// userAuthoredLM returns lm with default-overlaid values stripped: registry
// entries and role defaults that came verbatim from the embedded default config
// (Builder.OverlayDefaultRegistry, recorded as overlay) are runtime fallbacks,
// not user configuration. Persisting them would pin the user to a snapshot of
// shipped model defaults that stops tracking future releases. Anything the user
// added or changed since the overlay survives.
func userAuthoredLM(lm LMConfig, overlay *LMConfig) LMConfig {
	if overlay == nil {
		return lm
	}
	configs := make(map[string]LLMConfig, len(lm.Configs))
	for label, entry := range lm.Configs {
		if def, ok := overlay.Configs[label]; ok && reflect.DeepEqual(entry, def) {
			continue
		}
		configs[label] = entry
	}
	lm.Configs = configs
	if overlay.Defaults.Primary != "" && lm.Defaults.Primary == overlay.Defaults.Primary {
		lm.Defaults.Primary = ""
	}
	if overlay.Defaults.Fast != "" && lm.Defaults.Fast == overlay.Defaults.Fast {
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
