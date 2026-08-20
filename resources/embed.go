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
	return getPromptText(resourcesFS, name)
}

// getPromptText is GetPromptText over an injected filesystem, so the
// present-but-empty case can be exercised: the embedded FS is fixed at build
// time and cannot be made to hold one.
func getPromptText(fsys fs.FS, name string) (string, error) {
	b, err := fs.ReadFile(fsys, "prompts/"+name+".md")
	if err != nil {
		return "", err
	}
	text := strings.TrimRight(string(b), "\n")
	if strings.TrimSpace(text) == "" {
		// A present-but-empty prompt file is a build defect, not a prompt
		// with nothing to say: returning ("", nil) wired a zero-length system
		// prompt into a live session with no signal anywhere, and let
		// MustGetPromptText ship exactly the empty prompt its own doc
		// promises to panic over.
		return "", fmt.Errorf("resources: embedded prompt %q is empty", name)
	}
	return text, nil
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

// GetProfileSchema returns the embedded profile schema, the contract for a
// .ctxloom/profiles/<name>.yaml file (and for a bundle-shipped profile, which
// is the same document read from elsewhere).
//
// It exists as its own accessor beside GetConfigSchema because a profile is not
// a config section any more: the inline `profiles:` block was retired, so the
// profile object left config-schema.json with it, and this is where its
// additionalProperties:false lives now.
func GetProfileSchema() ([]byte, error) {
	return resourcesFS.ReadFile("schema/input/profile-schema.json")
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
	_, body := SplitCommandFrontmatter(string(raw))
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

// SplitCommandFrontmatter extracts a builtin command's optional YAML
// frontmatter ("---\ndescription: ...\n---\n<body>") and returns
// (description, body). A file with no frontmatter returns ("", the whole
// content) unchanged. Deliberately minimal: only the description key, which is
// the only key a builtin command carries.
//
// It is exported because the SAME embedded files are consumed two ways — as a
// prompt body through GetBuiltinCommandBody here, and as a slash-command
// export through internal/lm/backends.builtinCommands — and both must agree
// about where the frontmatter ends. That package imports this one, so the
// parser lives here, next to the bytes it parses; the reverse direction would
// cycle.
func SplitCommandFrontmatter(content string) (description, body string) {
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
	if len(names) == 0 {
		// These directories are embedded at BUILD time, so zero names never
		// means "the user has none" — it means the binary shipped without
		// content it is supposed to carry (an emptied directory, or files
		// that no longer match ext). Returning (nil, nil) made that
		// indistinguishable from a legitimately empty set at every caller,
		// all of which warn and degrade on an error and did nothing at all
		// on the silent nil.
		return nil, fmt.Errorf("resources: embedded %s/ holds no %s files", dir, ext)
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

// BuiltinBundlesFS returns the embedded builtin_bundles/ directory as a
// filesystem, so the one reader that reads bundle documents out of directories
// can serve the builtins too rather than a second body doing the same walk,
// parse and signature check.
//
// A build that embedded nothing is a build defect, not an empty set — but this
// accessor cannot report it and must not invent an alternative, so it hands
// back an empty FS and lets the reader's own "no bundles here" reporting say
// so. fs.Sub over an embed.FS with a literal, always-present path cannot fail;
// the error is folded into an empty FS rather than a panic in a library.
func BuiltinBundlesFS() fs.FS {
	sub, err := fs.Sub(resourcesFS, "builtin_bundles")
	if err != nil {
		return emptyFS{}
	}
	return sub
}

// emptyFS is the unreachable fallback above: a filesystem holding nothing.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
