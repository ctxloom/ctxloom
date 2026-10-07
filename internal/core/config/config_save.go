package config

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

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
	for _, key := range persistedKeys() {
		delete(merged, key)
	}
	maps.Copy(merged, desired)

	// Persist by PATCHING the on-disk document's yaml.Node tree so comments and
	// key order survive — only a section whose canonical content actually changed
	// is re-encoded, exactly like the comment-preserving upgrade path, rather than
	// re-emitting a sorted, comment-stripped map[string]interface{} marshal on
	// every write (U049-F16). A first write (no existing bytes) emits a fresh
	// document with every key sorted: the same bytes yaml.Marshal(c.Authored())
	// produces.
	data, err := marshalPreservingComments(existingData, merged)
	if err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// The config read never creates the app dir, so this may be its first write.
	if err := fs.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := safefs.WriteFile(fs, configPath, data, 0o644); err != nil {
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

// effectiveDoc is c as every run resolves it: toDoc's lossless copy, role and
// the shipped default registry included, with the format generation stamped
// current (load has already migrated whatever the file held). `config show`, whole
// or by section, renders it, so they describe the configuration actually in
// force rather than only the part a file spells out.
func (c *Config) effectiveDoc() configDoc {
	d := c.toDoc()
	d.SchemaVersion = CurrentConfigVersion
	return d
}

// persistedDoc is effectiveDoc without the shipped default registry
// (userAuthoredLM): what every save and init's scaffold write emit, and what
// `config show --raw` prints. The registry is left out because writing it
// would freeze one release's model defaults into every saved config.
// Everything else — role included — is written exactly as the layer holds it.
func (c *Config) persistedDoc() configDoc {
	d := c.effectiveDoc()
	d.LM = userAuthoredLM(d.LM, c.lmDefaultOverlay)
	return d
}

// Authored returns c as a save writes it (persistedDoc), for callers that
// render or write the file's document rather than the effective one. It
// marshals through the same configDoc.MarshalYAML as c itself.
func (c *Config) Authored() yaml.Marshaler { return authoredView{c} }

type authoredView struct{ c *Config }

// MarshalYAML returns the configDoc itself, like Config.MarshalYAML, so
// `config show --raw <section>` can reflect a section out of it by yaml tag.
func (v authoredView) MarshalYAML() (any, error) { return v.c.persistedDoc(), nil }

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
