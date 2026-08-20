package main

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Project-config validation
//
// These exercise the gate over the ONE optional target -- the gitignored
// .ctxloom/config.yaml -- in an isolated project dir. run() itself also checks
// the repo's tracked documents (see gate_test.go), which do not exist under a
// temp CWD, so these drive validateAll directly with the project target alone.
// =============================================================================

// projectOnly is the gate reduced to its optional project-config target.
func projectOnly() []target {
	return []target{{path: paths.ConfigPath(paths.AppDirName), optional: true}}
}

// TestProjectConfig_AbsentWithNothingElseIsAFailure is the rewritten
// TestRun_NoConfigFile, which pinned as intended behaviour: it asserted
// that finding no config was "a valid state ... and should not return an error".
// An uninitialised project genuinely has no config -- but a GATE that validated
// nothing has not gated anything, and in CI that was every single run. The
// absent optional target is still skipped; the empty verdict is what fails.
func TestProjectConfig_AbsentWithNothingElseIsAFailure(t *testing.T) {
	// A temp directory without any config.
	testsupport.ProjectDir(t)

	n, err := validateAll(projectOnly())
	assert.Zero(t, n)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validated no documents")
}

// TestRun_ValidConfig verifies that a properly structured config file passes
// validation. This confirms the happy path where users have correctly
// configured their ctxloom setup.
func TestProjectConfig_ValidConfig(t *testing.T) {
	testsupport.ProjectDir(t)

	// Create .ctxloom directory with valid config
	require.NoError(t, os.MkdirAll(paths.AppDirName, 0755))
	validConfig := `
version: 6
default_agent: default
agents:
  default:
    llm: primary
    profiles: [default]
llm:
  configs:
    primary: { type: claude-code }
  defaults:
    primary: primary
`
	require.NoError(t, os.WriteFile(paths.ConfigPath(paths.AppDirName), []byte(validConfig), 0644))

	n, err := validateAll(projectOnly())
	assert.NoError(t, err)
	assert.Equal(t, 1, n)
}

// TestRun_InvalidYAMLSyntax verifies that malformed YAML is detected and
// reported with a clear error. Users need actionable feedback when their
// config files have syntax errors.
func TestProjectConfig_InvalidYAMLSyntax(t *testing.T) {
	testsupport.ProjectDir(t)

	require.NoError(t, os.MkdirAll(paths.AppDirName, 0755))
	invalidYAML := `
default_profile: [invalid
`
	require.NoError(t, os.WriteFile(paths.ConfigPath(paths.AppDirName), []byte(invalidYAML), 0644))

	_, err := validateAll(projectOnly())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config.yaml")
}

// TestRun_SchemaViolation verifies that configs which violate the JSON schema
// are rejected. This catches semantic errors like unknown fields or wrong
// types that YAML parsing alone wouldn't detect.
func TestProjectConfig_SchemaViolation(t *testing.T) {
	testsupport.ProjectDir(t)

	require.NoError(t, os.MkdirAll(paths.AppDirName, 0755))
	// Use an invalid type for default_profiles (should be array, not string)
	invalidSchema := `
default_profiles: "not-an-array"
`
	require.NoError(t, os.WriteFile(paths.ConfigPath(paths.AppDirName), []byte(invalidSchema), 0644))

	_, err := validateAll(projectOnly())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation error")
}

// TestRun_EmptyObjectConfig verifies that an empty object config file is valid.
// An empty object {} is the minimal valid state after initialization.
func TestProjectConfig_EmptyObjectConfig(t *testing.T) {
	testsupport.ProjectDir(t)

	require.NoError(t, os.MkdirAll(paths.AppDirName, 0755))
	// Schema requires an object, so {} is minimal valid config
	require.NoError(t, os.WriteFile(paths.ConfigPath(paths.AppDirName), []byte("{}"), 0644))

	n, err := validateAll(projectOnly())
	assert.NoError(t, err)
	assert.Equal(t, 1, n)
}

// TestRun_NullConfig verifies that a null/empty YAML file is rejected.
// The schema requires a valid object, not null.
func TestProjectConfig_NullConfig(t *testing.T) {
	testsupport.ProjectDir(t)

	require.NoError(t, os.MkdirAll(paths.AppDirName, 0755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(paths.AppDirName), []byte(""), 0644))

	_, err := validateAll(projectOnly())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema validation error")
}

// TestRun_ComplexValidConfig verifies that a fully-featured config with
// multiple profiles, plugins, and hooks passes validation.
// This represents a production-ready configuration.
func TestProjectConfig_ComplexValidConfig(t *testing.T) {
	testsupport.ProjectDir(t)

	require.NoError(t, os.MkdirAll(paths.AppDirName, 0755))
	require.NoError(t, os.MkdirAll(paths.LocalBundlesPath(paths.AppDirName), 0755))

	complexConfig := `
version: 3

config:
  use_distilled: true

llm:
  configs:
    big:    { type: claude-code, model: opus }
    g:      { type: codex, model: gpt-5-codex }
  defaults:
    primary: big
    fast: big

editor:
  command: vim
  args:
    - -n

default_agent: dev
agents:
  dev:
    llm: big
    profiles:
      - development
      - testing
`
	require.NoError(t, os.WriteFile(paths.ConfigPath(paths.AppDirName), []byte(complexConfig), 0644))

	n, err := validateAll(projectOnly())
	assert.NoError(t, err)
	assert.Equal(t, 1, n)
}
