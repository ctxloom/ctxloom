package bundles

import (
	"gopkg.in/yaml.v3"
	"reflect"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// bundleUpgrades is the canonical, ordered bundle schema upgrade pipeline,
// oldest-first. ParseBundle runs it over raw bundle YAML on every load so older
// on-disk and remote-seeded bundles normalize to the current schema in memory —
// ctxloom upgrades on load rather than silently dropping a renamed key. Append
// an Upgrader here as the bundle schema evolves; each must be idempotent.
var bundleUpgrades = upgrade.Pipeline{
	commandsKeyUpgrade{},
	exportsKeyUpgrade{},
}

// commandsKeyUpgrade renames the legacy top-level `prompts:` map key to
// `commands:`. The bundle item-kind "prompt" was renamed to "skill" and then
// to "command" (so Bundle now unmarshals its slash-command items from
// `commands:`); without this migration an older bundle's `prompts:` block
// would be silently dropped on load. This is a one-hop rewrite straight from
// `prompts:` to `commands:` — it never lands on the intermediate `skills:`
// name, because `skills:` is reserved for a different, future item-kind
// (Agent Skills) that a permanent rewrite would corrupt.
type commandsKeyUpgrade struct{}

// Name identifies the upgrade in logs.
func (commandsKeyUpgrade) Name() string { return "rename bundle prompts to commands" }

// Apply renames the top-level prompts key to commands. Idempotent: a bundle
// already using `commands:` (or with no prompts) is left untouched.
func (commandsKeyUpgrade) Apply(root *yaml.Node) bool {
	return renameMapKey(root, "prompts", "commands")
}

// exportsKeyUpgrade renames a command's or skill's per-engine export block
// from the retired `llm:` key to `exports:`. The block's shape is unchanged
// (engine name → block); only the key moves, so an authored bundle in the
// old spelling reads to the same blocks the writer now emits.
type exportsKeyUpgrade struct{}

// Name identifies the upgrade in logs.
func (exportsKeyUpgrade) Name() string { return "rename item llm to exports" }

// Apply renames `llm` to `exports` on every item of every kind that carries
// export blocks. Idempotent: an item already spelling `exports:` (or
// carrying neither) is left untouched.
func (exportsKeyUpgrade) Apply(root *yaml.Node) bool {
	kinds := exportCarryingKeys()
	changed := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if !kinds[root.Content[i].Value] {
			continue
		}
		items := root.Content[i+1]
		if items.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(items.Content); j += 2 {
			if item := items.Content[j+1]; item.Kind == yaml.MappingNode && renameMapKey(item, "llm", "exports") {
				changed = true
			}
		}
	}
	return changed
}

// exportCarryingKeys are the top-level bundle keys whose items carry
// EngineBlocks, read off Bundle's own declaration so the upgrade follows the
// schema: a kind that gains an exports field is upgraded without this file
// learning its name, and a profile's scalar `llm:` (the engine label) is
// never touched because a profile carries no blocks.
func exportCarryingKeys() map[string]bool {
	blocks := reflect.TypeFor[EngineBlocks]()
	out := map[string]bool{}
	bundle := reflect.TypeFor[Bundle]()
	for i := 0; i < bundle.NumField(); i++ {
		f := bundle.Field(i)
		if f.Type.Kind() != reflect.Map {
			continue
		}
		item := f.Type.Elem()
		if item.Kind() != reflect.Struct {
			continue
		}
		for j := 0; j < item.NumField(); j++ {
			if item.Field(j).Type == blocks {
				out[yamlFieldName(f)] = true
				break
			}
		}
	}
	return out
}

// renameMapKey renames oldKey to newKey on a mapping node in place (preserving
// the key's position), reporting whether it changed anything. When newKey is
// already present the legacy oldKey pair is dropped instead (the current key
// wins), so the result never carries a duplicate key. Idempotent: a node
// without oldKey is untouched.
func renameMapKey(root *yaml.Node, oldKey, newKey string) bool {
	oldIdx, hasNew := -1, false
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case oldKey:
			oldIdx = i
		case newKey:
			hasNew = true
		}
	}
	if oldIdx == -1 {
		return false
	}
	if hasNew {
		root.Content = append(root.Content[:oldIdx], root.Content[oldIdx+2:]...)
		return true
	}
	root.Content[oldIdx].Value = newKey
	return true
}
