// Package resources provides embedded static files for ctxloom.
package resources

import (
	"embed"
)

//go:embed all:schema all:commands all:builtin_bundles example-config.yaml default-remotes.yaml
var resourcesFS embed.FS

// GetConfigSchema returns the embedded JSON schema for config validation.
func GetConfigSchema() ([]byte, error) {
	return resourcesFS.ReadFile("schema/config-schema.json")
}

// GetExampleConfig returns the embedded example config file.
func GetExampleConfig() ([]byte, error) {
	return resourcesFS.ReadFile("example-config.yaml")
}

// GetDefaultRemotes returns the embedded default remotes file.
func GetDefaultRemotes() ([]byte, error) {
	return resourcesFS.ReadFile("default-remotes.yaml")
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
// tasks auto-capture + plan-stamping hooks, skill prompts) so users get it
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
