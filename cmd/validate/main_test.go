package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// run() Tests
//
// The run function is the core entry point for config validation. These tests
// ensure that schema validation works correctly for various config file states.
// =============================================================================

// TestRun_NoConfigFile verifies that the validator handles the case where no
// config file exists. This is a valid state for a project that hasn't been
// initialized with SCM yet, and should not return an error.
func TestRun_NoConfigFile(t *testing.T) {
	// Change to a temp directory without any config
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	err = run()
	assert.NoError(t, err)
}

// TestRun_ValidConfig verifies that a properly structured config file passes
// validation. This confirms the happy path where users have correctly
// configured their SCM setup.
func TestRun_ValidConfig(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	// Create .scm directory and valid config
	require.NoError(t, os.MkdirAll(".scm", 0755))
	validConfig := `
defaults:
  profiles:
    - default
  llm_plugin: mock
`
	require.NoError(t, os.WriteFile(".scm/config.yaml", []byte(validConfig), 0644))

	err = run()
	assert.NoError(t, err)
}

// TestRun_InvalidYAMLSyntax verifies that malformed YAML is detected and
// reported with a clear error. Users need actionable feedback when their
// config files have syntax errors.
func TestRun_InvalidYAMLSyntax(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	require.NoError(t, os.MkdirAll(".scm", 0755))
	invalidYAML := `
default_profile: [invalid
`
	require.NoError(t, os.WriteFile(".scm/config.yaml", []byte(invalidYAML), 0644))

	err = run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config.yaml")
}

// TestRun_SchemaViolation verifies that configs which violate the JSON schema
// are rejected. This catches semantic errors like unknown fields or wrong
// types that YAML parsing alone wouldn't detect.
func TestRun_SchemaViolation(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	require.NoError(t, os.MkdirAll(".scm", 0755))
	// Use an invalid type for default_profiles (should be array, not string)
	invalidSchema := `
default_profiles: "not-an-array"
`
	require.NoError(t, os.WriteFile(".scm/config.yaml", []byte(invalidSchema), 0644))

	err = run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation error")
}

// TestRun_EmptyObjectConfig verifies that an empty object config file is valid.
// An empty object {} is the minimal valid state after initialization.
func TestRun_EmptyObjectConfig(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	require.NoError(t, os.MkdirAll(".scm", 0755))
	// Schema requires an object, so {} is minimal valid config
	require.NoError(t, os.WriteFile(".scm/config.yaml", []byte("{}"), 0644))

	err = run()
	assert.NoError(t, err)
}

// TestRun_NullConfig verifies that a null/empty YAML file is rejected.
// The schema requires a valid object, not null.
func TestRun_NullConfig(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	require.NoError(t, os.MkdirAll(".scm", 0755))
	require.NoError(t, os.WriteFile(".scm/config.yaml", []byte(""), 0644))

	err = run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation error")
}

// TestRun_ComplexValidConfig verifies that a fully-featured config with
// multiple profiles, plugins, and hooks passes validation.
// This represents a production-ready configuration.
func TestRun_ComplexValidConfig(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	require.NoError(t, os.MkdirAll(".scm", 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(".scm", "bundles"), 0755))

	complexConfig := `
version: "1.0"

defaults:
  profiles:
    - development
    - testing
  llm_plugin: claudecode
  use_distilled: true

llm:
  plugin_paths:
    - /home/user/.scm/plugins
  plugins:
    claudecode:
      model: opus
    gemini:
      model: gemini-pro

editor:
  command: vim
  args:
    - -n

profiles:
  development:
    description: Development profile with common tools
    fragments:
      - go-development
    tags:
      - dev
  testing:
    description: Testing profile
    parents:
      - development
    fragments:
      - test-helpers
`
	require.NoError(t, os.WriteFile(".scm/config.yaml", []byte(complexConfig), 0644))

	err = run()
	assert.NoError(t, err)
}
