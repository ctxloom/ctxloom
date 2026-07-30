// Package resources provides embedded static files for ctxloom.
package resources

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// schema/gen/ is deliberately EXCLUDED: it is `just gen-schemas`'s gitignored,
// reflected-from-Go-structs output (schemagen.md), and no accessor in this
// package — nor any caller anywhere in the repo — ever reads a "schema/gen/"
// path; GetSchema/GetConfigSchema only ever resolve "schema/input/...". Before
// this fix, `all:schema` recursively embedded schema/gen/ too, shipping ~70
// unreachable generated JSON Schema files (~284 KB) in every binary. Embedding
// schema/input directly (rather than all:schema) keeps the three hand-authored,
// actually-read schemas and drops the dead weight.
//
//go:embed all:schema/input all:commands all:builtin_bundles all:prompts all:profiles example-config.yaml default-config.yaml init-config.yaml default-remotes.yaml
var resourcesFS embed.FS

// GetPromptText returns an embedded prompt/instruction template by name
// (resources/prompts/<name>.md). These hold file-sized prompt content — LLM
// system prompts and MCP instructions — that would otherwise live as
// hand-escaped Go string literals. The trailing newline is trimmed so callers
// get the same shape regardless of the file's final newline.
func GetPromptText(name string) (string, error) {
	b, err := resourcesFS.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// MustGetPromptText is GetPromptText for package-level initialization, where a
// missing embedded prompt is a build-time bug (the file is compiled in), not a
// runtime condition. It panics rather than shipping an empty prompt.
func MustGetPromptText(name string) string {
	s, err := GetPromptText(name)
	if err != nil {
		panic(fmt.Sprintf("resources: embedded prompt %q: %v", name, err))
	}
	return s
}

// GetConfigSchema returns the embedded JSON schema for config validation.
func GetConfigSchema() ([]byte, error) {
	return resourcesFS.ReadFile("schema/input/config-schema.json")
}

// GetSchema returns an embedded published JSON Schema by file name
// (e.g. "run-oneshot-result-schema.json"). Schemas under schema/ are the
// stable, documented contracts for ctxloom's JSON output; each is kept in sync
// with its producing Go struct by a drift test. This is the generic accessor
// the per-surface schemas (vexed-cloak) are read through.
func GetSchema(name string) ([]byte, error) {
	return resourcesFS.ReadFile("schema/" + name)
}

// GetExampleConfig returns the embedded example config file.
func GetExampleConfig() ([]byte, error) {
	return resourcesFS.ReadFile("example-config.yaml")
}

// GetDefaultConfig returns the embedded default config overlaid on every
// loaded user config, defining the built-in primary + fast LLM roles so an
// empty user config still resolves a backend and model.
func GetDefaultConfig() ([]byte, error) {
	return resourcesFS.ReadFile("default-config.yaml")
}

// GetInitConfig returns the embedded scaffolding config `ctxloom init` starts
// from: static defaults (version, settings, mcp) with no llm block — init fills
// the llm registry from the selected engine before writing.
func GetInitConfig() ([]byte, error) {
	return resourcesFS.ReadFile("init-config.yaml")
}

// GetDefaultRemotes returns the embedded default remotes file.
func GetDefaultRemotes() ([]byte, error) {
	return resourcesFS.ReadFile("default-remotes.yaml")
}

// GetSeedProfile returns an embedded profile template `ctxloom init` scaffolds
// into a fresh project's .ctxloom/profiles/<name>.yaml. These are starting-point
// LOCAL profiles (not bare remote refs), so the user owns and can edit the
// default that `ctxloom run` assembles.
func GetSeedProfile(name string) ([]byte, error) {
	return resourcesFS.ReadFile("profiles/" + name + ".yaml")
}

// GetBuiltinCommand returns an embedded builtin command by name.
func GetBuiltinCommand(name string) ([]byte, error) {
	return resourcesFS.ReadFile("commands/" + name + ".md")
}

// GetBuiltinCommandBody returns an embedded builtin command's BODY — the
// content after its "---\ndescription: ...\n---" frontmatter is stripped —
// for callers that want to consume the command's own prompt text directly
// (e.g. composing it into another session's prompt) rather than exporting it
// as a slash-command file. This is a second consumption path onto the SAME
// embedded file `internal/lm/backends.builtinCommands` exports as a slash
// command; the two must never drift, so both read the identical bytes from
// this package rather than each keeping their own copy.
func GetBuiltinCommandBody(name string) (string, error) {
	raw, err := GetBuiltinCommand(name)
	if err != nil {
		return "", err
	}
	_, body := splitCommandFrontmatter(string(raw))
	return body, nil
}

// MustGetBuiltinCommandBody is GetBuiltinCommandBody for package-level
// initialization, where a missing embedded command is a build-time bug (the
// file is compiled in), not a runtime condition. It panics rather than
// shipping an empty prompt — mirrors MustGetPromptText.
func MustGetBuiltinCommandBody(name string) string {
	body, err := GetBuiltinCommandBody(name)
	if err != nil {
		panic(fmt.Sprintf("resources: embedded command %q: %v", name, err))
	}
	return body
}

// splitCommandFrontmatter extracts a builtin command's optional YAML
// frontmatter ("---\ndescription: ...\n---\n<body>") and returns
// (description, body). A file with no frontmatter returns ("", the whole
// content) unchanged. Deliberately minimal (only the description key) —
// mirrors internal/lm/backends.parseMarkdownFrontmatter, which does the same
// parse for slash-command export; kept as a small separate copy here rather
// than an import (that package already imports resources, so the reverse
// would cycle).
func splitCommandFrontmatter(content string) (description, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}
	rest := content[4:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return "", content
	}
	frontmatter := rest[:endIdx]
	body = strings.TrimPrefix(rest[endIdx+4:], "\n")
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			if len(description) >= 2 && description[0] == '"' && description[len(description)-1] == '"' {
				description = description[1 : len(description)-1]
			}
			break
		}
	}
	return description, body
}

// listEmbeddedNames returns the extension-stripped names of every file
// directly under dir whose name ends in ext, in ReadDir's (sorted) order. It
// is the one implementation the per-kind listers below share.
//
// The length check is not redundant with the suffix check: a file whose ENTIRE
// name is the extension (".md") suffixes correctly and strips to the empty
// string, which is not a name any caller can then look up. Such a file is not
// a named item and is skipped, as are directories.
func listEmbeddedNames(fsys fs.FS, dir, ext string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || len(n) <= len(ext) || !strings.HasSuffix(n, ext) {
			continue
		}
		names = append(names, strings.TrimSuffix(n, ext))
	}
	return names, nil
}

// ListBuiltinCommands returns the names of all embedded builtin commands.
func ListBuiltinCommands() ([]string, error) {
	return listEmbeddedNames(resourcesFS, "commands", ".md")
}

// GetBuiltinBundle returns the raw YAML bytes for a built-in bundle embedded
// in the binary. Built-in bundles ship core ctxloom functionality (e.g.
// session-bind + plan-stamping hooks, command prompts) so users get it
// without needing to pull anything from a remote.
func GetBuiltinBundle(name string) ([]byte, error) {
	return resourcesFS.ReadFile("builtin_bundles/" + name + ".yaml")
}

// ListBuiltinBundles returns the names of all built-in bundles embedded in
// the binary.
func ListBuiltinBundles() ([]string, error) {
	return listEmbeddedNames(resourcesFS, "builtin_bundles", ".yaml")
}
