package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MockLM provides a fake language model for testing.
// Uses the built-in mock plugin for gRPC-based plugin system.
type MockLM struct {
	// BinaryPath is kept for compatibility but unused with gRPC plugins
	BinaryPath string

	// Response is what the mock LM will output
	Response string

	// ExitCode is what the mock LM will return
	ExitCode int

	// RecordedInputPath is where the mock records received input
	RecordedInputPath string

	// ProjectDir is the project directory containing .scm/config.yaml
	ProjectDir string
}

// NewMockLM creates a new mock LM in the given directory.
// The dir parameter is the root temp directory; projectDir will be set by SetupMockLM.
func NewMockLM(dir string) (*MockLM, error) {
	m := &MockLM{
		BinaryPath:        filepath.Join(dir, "mock-lm"), // Kept for compatibility
		Response:          "Mock LM response",
		ExitCode:          0,
		RecordedInputPath: filepath.Join(dir, "mock-lm-input.txt"),
		ProjectDir:        "", // Will be set by SetupMockLM
	}

	// No longer need to write a shell script; using built-in mock plugin
	return m, nil
}

// Write creates/updates the mock LM script with current settings.
func (m *MockLM) Write() error {
	// Create a shell script that:
	// 1. Records all input to a file
	// 2. Outputs the configured response
	// 3. Exits with the configured code
	script := fmt.Sprintf(`#!/bin/sh
# Mock LM for testing

# Record all arguments and stdin
{
    echo "=== Arguments ==="
    for arg in "$@"; do
        echo "$arg"
    done
    echo "=== Stdin ==="
    cat
} > "%s"

# Output response
cat << 'MOCK_RESPONSE_EOF'
%s
MOCK_RESPONSE_EOF

exit %d
`, m.RecordedInputPath, m.Response, m.ExitCode)

	if err := os.WriteFile(m.BinaryPath, []byte(script), 0755); err != nil {
		return fmt.Errorf("failed to write mock LM script: %w", err)
	}

	return nil
}

// SetResponse sets the response. Config will be updated on next SetupMockLM call.
func (m *MockLM) SetResponse(response string) error {
	m.Response = response
	// For the new gRPC-based mock, we need to update the config file
	return m.WriteConfig()
}

// SetExitCode sets the exit code. Config will be updated on next SetupMockLM call.
func (m *MockLM) SetExitCode(code int) error {
	m.ExitCode = code
	// For the new gRPC-based mock, we need to update the config file
	return m.WriteConfig()
}

// WriteConfig updates the mock plugin configuration in .scm/config.yaml.
// Preserves existing profiles and other config while updating mock plugin settings.
func (m *MockLM) WriteConfig() error {
	if m.ProjectDir == "" {
		return fmt.Errorf("ProjectDir not set; call SetupMockLM first")
	}

	configPath := filepath.Join(m.ProjectDir, ".scm", "config.yaml")

	// Read existing config if present
	existingConfig := ""
	if data, err := os.ReadFile(configPath); err == nil {
		existingConfig = string(data)
	}

	// Extract sections to preserve (profiles only - defaults will be rebuilt)
	profilesSection := extractYAMLSection(existingConfig, "profiles:")

	// Build config with mock settings
	var config strings.Builder
	config.WriteString("llm:\n")
	config.WriteString("  plugins:\n")
	config.WriteString("    mock:\n")
	config.WriteString("      args: []\n")
	config.WriteString("      env:\n")
	_, _ = fmt.Fprintf(&config, "        scm_mock_record_file: \"%s\"\n", m.RecordedInputPath)
	_, _ = fmt.Fprintf(&config, "        scm_mock_response: \"%s\"\n", escapeYAMLString(m.Response))
	_, _ = fmt.Fprintf(&config, "        scm_mock_exit_code: \"%d\"\n", m.ExitCode)

	// Always set llm_plugin to mock
	config.WriteString("defaults:\n")
	config.WriteString("  llm_plugin: mock\n")
	config.WriteString("  use_distilled: false\n")

	if profilesSection != "" {
		config.WriteString(profilesSection)
	} else {
		config.WriteString("profiles: {}\n")
	}

	return os.WriteFile(configPath, []byte(config.String()), 0644)
}

// extractYAMLSection extracts a top-level YAML section from config content.
// Returns empty string if section not found.
func extractYAMLSection(config, sectionKey string) string {
	idx := strings.Index(config, sectionKey)
	if idx < 0 {
		return ""
	}

	// Find where this section ends (next top-level key or end of file)
	section := config[idx:]
	lines := strings.Split(section, "\n")

	var result strings.Builder
	for i, line := range lines {
		if i == 0 {
			result.WriteString(line)
			result.WriteString("\n")
			continue
		}
		// Stop at next top-level key (line starting with non-space, non-empty)
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
			break
		}
		result.WriteString(line)
		result.WriteString("\n")
	}

	return result.String()
}

// GetRecordedInput returns the input that was sent to the mock LM.
func (m *MockLM) GetRecordedInput() (string, error) {
	data, err := os.ReadFile(m.RecordedInputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// ClearRecordedInput removes the recorded input file.
func (m *MockLM) ClearRecordedInput() error {
	err := os.Remove(m.RecordedInputPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// SetupMockLM sets up a mock LM in the test environment and configures scm to use it.
// Uses the built-in mock plugin for gRPC-based plugin system.
func (e *TestEnvironment) SetupMockLM() (*MockLM, error) {
	mockLM, err := NewMockLM(e.Root)
	if err != nil {
		return nil, err
	}

	// Set the project directory so WriteConfig knows where to write
	mockLM.ProjectDir = e.ProjectDir

	// Write the initial config
	if err := mockLM.WriteConfig(); err != nil {
		return nil, err
	}

	return mockLM, nil
}

// escapeYAMLString escapes special characters for YAML string values.
func escapeYAMLString(s string) string {
	// Replace newlines and quotes for safe YAML embedding
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
