// Package resources provides embedded static files for ctxloom.
package resources

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed all:schema all:commands all:builtin_bundles all:prompts all:profiles example-config.yaml default-config.yaml init-config.yaml default-remotes.yaml
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

// ListBuiltinCommands returns the names of all embedded builtin commands.
func ListBuiltinCommands() ([]string, error) {
	entries, err := resourcesFS.ReadDir("commands")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 3 && e.Name()[len(e.Name())-3:] == ".md" {
			names = append(names, e.Name()[:len(e.Name())-3])
		}
	}
	return names, nil
}

// GetBuiltinBundle returns the raw YAML bytes for a built-in bundle embedded
// in the binary. Built-in bundles ship core ctxloom functionality (e.g.
// session-bind + plan-stamping hooks, skill prompts) so users get it
// without needing to pull anything from a remote.
func GetBuiltinBundle(name string) ([]byte, error) {
	return resourcesFS.ReadFile("builtin_bundles/" + name + ".yaml")
}

// ListBuiltinBundles returns the names of all built-in bundles embedded in
// the binary.
func ListBuiltinBundles() ([]string, error) {
	entries, err := resourcesFS.ReadDir("builtin_bundles")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if len(n) > 5 && n[len(n)-5:] == ".yaml" {
			names = append(names, n[:len(n)-5])
		}
	}
	return names, nil
}
