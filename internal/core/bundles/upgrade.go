package bundles

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// envelopeKind versions a bundle's envelope (bundle.yaml), and through it the
// whole tree: item files carry no format key of their own.
//
// No LegacyKey, and that is not an omission: an envelope's `version` is its
// AUTHOR'S semver release, never a format generation, so it must never be read
// as one or renamed.
//
// Generation 0 is every envelope that declares no schemaver.Key — which is
// every bundle signed before the key existed, so it must keep loading exactly
// as it always has. ParseBundle runs the Upgrade on the raw bytes, after any
// signature check and before the strict decode, and nothing is re-signed or
// written back implicitly (see persistEnvelopeUpgrade and UpgradeEnvelopeAt).
var envelopeKind = schemaver.Kind{
	Name:   "bundle",
	Oldest: 0,
	Steps:  []upgrade.Upgrader{retiredKeysStep{}, profileRefsStep{}, execItemFieldsStep{}},
}

// execItemFieldsGeneration is the generation execItemFieldsStep migrates an
// envelope TO.
const execItemFieldsGeneration = 3

// execItemFieldsStep is generation 2 -> 3: a tree's MCP items may declare
// url, headers and tags, and its hook items tags. Nothing an older tree holds
// changes meaning, so the step edits nothing; the generation exists so a
// binary that cannot read those keys refuses the tree (schemaver.ErrNewer)
// rather than loading a remote server with no target or an item with its
// link membership silently dropped.
type execItemFieldsStep struct{}

func (execItemFieldsStep) Name() string {
	return "mcp items gain url, headers and tags; hook items gain tags"
}

func (execItemFieldsStep) Apply(*yaml.Node) bool { return false }

// profileRefsGeneration is the generation profileRefsStep migrates an
// envelope TO. A tree whose envelope declares an older one has its profile
// items migrated by the same rule (ReadTree, migrateProfileItems), because
// item files carry no format key of their own.
const profileRefsGeneration = 2

// profileRefsStep is generation 1 -> 2: every profile's stored bundle and
// parent references move onto the canonical ctxloom URI grammar
// (profiles.CanonicalRefs). On an envelope it rewrites the profiles an
// inline document carries.
type profileRefsStep struct{}

func (profileRefsStep) Name() string { return "profiles: " + profiles.CanonicalRefs.Name() }

func (profileRefsStep) Apply(root *yaml.Node) bool {
	items := yamlx.MapValue(root, "profiles")
	if items == nil || items.Kind != yaml.MappingNode {
		return false
	}
	changed := false
	for i := 1; i < len(items.Content); i += 2 {
		if items.Content[i].Kind == yaml.MappingNode && profiles.CanonicalRefs.Apply(items.Content[i]) {
			changed = true
		}
	}
	return changed
}

// migrateProfileItems rewrites the profile item files of the tree at dir in
// place by profiles.CanonicalRefs — the item half of profileRefsStep, for a
// writer that persists an envelope's migration past profileRefsGeneration:
// stamping the envelope current while its items still spell the old grammar
// would claim a migration that never happened.
func migrateProfileItems(fsys afero.Fs, dir string) error {
	itemDir := filepath.Join(dir, paths.ProfilesDir)
	entries, err := afero.ReadDir(fsys, itemDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("bundles: reading %s: %w", itemDir, err)
	}
	step := upgrade.Pipeline{profiles.CanonicalRefs}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(itemDir, e.Name())
		raw, err := afero.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("bundles: reading %s: %w", path, err)
		}
		out, applied, err := step.Run(raw)
		if err != nil {
			return fmt.Errorf("bundles: migrating %s: %w", path, err)
		}
		if len(applied) == 0 {
			continue
		}
		if err := safefs.WriteFile(fsys, path, out, e.Mode().Perm()); err != nil {
			return fmt.Errorf("bundles: migrating %s: %w", path, err)
		}
	}
	return nil
}

// retiredKeysStep is generation 0 -> 1: the key renames a generation-0
// envelope may still spell.
type retiredKeysStep struct{}

func (retiredKeysStep) Name() string {
	return commandsKeyUpgrade{}.Name() + "; " + exportsKeyUpgrade{}.Name()
}

func (retiredKeysStep) Apply(root *yaml.Node) bool {
	commands := commandsKeyUpgrade{}.Apply(root)
	exports := exportsKeyUpgrade{}.Apply(root)
	return commands || exports
}

// resignToPersist is what --write-upgrades says instead of rewriting a signed
// tree's envelope: the signature covers those bytes, and nothing re-signs
// implicitly.
const resignToPersist = "re-sign to persist"

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

// Apply renames the top-level prompts key to commands. A bundle
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
