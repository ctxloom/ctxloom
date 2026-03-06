// Package resources provides embedded static files for SCM.
package resources

import (
	"embed"
)

//go:embed all:schema example-config.yaml
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
