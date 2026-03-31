// Package resources provides embedded static files for ctxloom.
package resources

import (
	"embed"
)

//go:embed all:schema all:commands example-config.yaml default-remotes.yaml
var resourcesFS embed.FS

// GetFragmentSchema returns the embedded JSON schema for fragment validation.
func GetFragmentSchema() ([]byte, error) {
	return resourcesFS.ReadFile("schema/fragment-schema.json")
}

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
