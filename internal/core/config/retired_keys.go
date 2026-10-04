package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// retiredKeys maps a dotted config path that a schema generation RETIRED to the
// guidance that names its replacement. It is the ONE table both readers use:
// configload's schema validation (where a key fires only when the migrator did
// NOT rewrite it — the "copied a stale doc into a current config" case) and
// ParseConfig, the init path, which skips validation.
var retiredKeys = map[string]string{
	"profiles": "the `profiles:` block was RETIRED: a profile is an item of the project bundle — write each definition to " +
		"a file named <name>.yaml, keeping its body verbatim (every field is spelled the same), and `ctxloom profile import` it. " +
		"The default context is whatever the default AGENT composes: `default_agent: <name>` and " +
		"`agents.<name>.profiles: [...]`",
	"defaults":       "the top-level `defaults` bag was RETIRED: use `llm.defaults.primary` / `llm.defaults.fast` for models, and `default_agent` for the default context",
	"llm.plugins":    "`llm.plugins` was RENAMED to `llm.configs`",
	"llm.default":    "`llm.default` was REPLACED by `llm.defaults.primary`",
	"llm.compaction": "`llm.compaction` was REPLACED by `llm.defaults.fast`",
	"subagents":      "`subagents` was RENAMED to `agents`",
	// Deliberately NOT migrated into the state record: a value from a
	// committed, env-overridable file is not a human's consent, so the old
	// grant is dropped (fails closed) and the user is told how to re-grant.
	// No migration: a base Containerfile has no isolation_base value to
	// become, so the user re-homes it by hand.
	"isolation_devcontainer_base": "`isolation_devcontainer_base` was REPLACED by `isolation_base`: " +
		"`isolation_devcontainer_base: false` becomes `isolation_base: ctxloom`; delete it otherwise " +
		"(an unset isolation_base already uses a detected devcontainer)",
	"isolation_base_containerfile": "`isolation_base_containerfile` was REPLACED by `isolation_base: ctxloom | devcontainer | <image ref>`: " +
		"move the Containerfile into the project devcontainer (`ctxloom container scaffold` writes one) " +
		"or build it and name the image, then delete this key",
	"dirty_tree_commit_ack": "`dirty_tree_commit_ack` was RETIRED: the consent is no longer a config key, and a value here " +
		"grants nothing. Re-grant it for this checkout with `ctxloom manage commit trust`, " +
		"or answer the dirty-tree question in `ctxloom init`",
}

// RetiredKeyMessage renders the user-visible line for a retired key at the
// dotted path in source, and whether path is retired at all.
func RetiredKeyMessage(path, source string) (string, bool) {
	hint, ok := retiredKeys[path]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("unknown key `%s` in %s: ctxloom does not know it, so it is IGNORED — %s", path, source, hint), true
}

// parsedDocumentSource names the document in a ParseConfig warning: it reads
// bytes, not a file, so there is no path to give.
const parsedDocumentSource = "the parsed config document"

// retiredKeyWarnings walks root for every retired path, in a stable order, and
// returns one unknown-key warning per path present.
func retiredKeyWarnings(root *yaml.Node, source string) []Warning {
	paths := make([]string, 0, len(retiredKeys))
	for p := range retiredKeys {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []Warning
	for _, p := range paths {
		node := root
		for _, seg := range strings.Split(p, ".") {
			node = mappingValue(node, seg)
		}
		if node == nil {
			continue
		}
		msg, _ := RetiredKeyMessage(p, source)
		out = append(out, Warning{Kind: WarnKindUnknownKey, Text: msg})
	}
	return out
}
