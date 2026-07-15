package bundles

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// bundleUpgrades is the canonical, ordered bundle schema upgrade pipeline,
// oldest-first. ParseBundle runs it over raw bundle YAML on every load so older
// on-disk and remote-seeded bundles normalize to the current schema in memory —
// ctxloom upgrades on load rather than silently dropping a renamed key. Append
// an Upgrader here as the bundle schema evolves; each must be idempotent.
var bundleUpgrades = upgrade.Pipeline{
	commandsKeyUpgrade{},
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
