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
	// Response is what the mock LM will output
	Response string

	// ExitCode is what the mock LM will return
	ExitCode int

	// RecordedInputPath is where the mock records received input
	RecordedInputPath string

	// ProjectDir is the project directory containing .ctxloom/config.yaml
	ProjectDir string
}

// NewMockLM creates a new mock LM in the given directory.
// The dir parameter is the root temp directory; projectDir will be set by SetupMockLM.
func NewMockLM(dir string) (*MockLM, error) {
	m := &MockLM{
		Response:          "Mock LM response",
		ExitCode:          0,
		RecordedInputPath: filepath.Join(dir, "mock-lm-input.txt"),
		ProjectDir:        "", // Will be set by SetupMockLM
	}

	// No longer need to write a shell script; using built-in mock plugin
	return m, nil
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

// WriteConfig updates the mock plugin configuration in .ctxloom/config.yaml.
// Preserves existing profiles and other config while updating mock plugin settings.
func (m *MockLM) WriteConfig() error {
	if m.ProjectDir == "" {
		return fmt.Errorf("ProjectDir not set; call SetupMockLM first")
	}

	configPath := filepath.Join(m.ProjectDir, ".ctxloom", "config.yaml")

	// Read existing config if present
	existingConfig := ""
	if data, err := os.ReadFile(configPath); err == nil {
		existingConfig = string(data)
	}

	// Extract sections to preserve (profiles only - defaults will be rebuilt)
	profilesSection := extractYAMLSection(existingConfig, "profiles:")

	// Build config with mock settings (schema v4: labeled configs + role map,
	// config.use_distilled under the config: section).
	var config strings.Builder
	config.WriteString("version: 4\n")
	config.WriteString("llm:\n")
	config.WriteString("  configs:\n")
	config.WriteString("    mock:\n")
	config.WriteString("      type: mock\n")
	config.WriteString("      env:\n")
	_, _ = fmt.Fprintf(&config, "        CTXLOOM_MOCK_RECORD_FILE: \"%s\"\n", m.RecordedInputPath)
	_, _ = fmt.Fprintf(&config, "        CTXLOOM_MOCK_RESPONSE: \"%s\"\n", escapeYAMLString(m.Response))
	_, _ = fmt.Fprintf(&config, "        CTXLOOM_MOCK_EXIT_CODE: \"%d\"\n", m.ExitCode)
	config.WriteString("  defaults:\n")
	config.WriteString("    primary: mock\n")

	config.WriteString("config:\n")
	config.WriteString("  use_distilled: false\n")

	if profilesSection != "" {
		config.WriteString(profilesSection)
	} else {
		config.WriteString("profiles: {}\n")
	}

	return os.WriteFile(configPath, []byte(config.String()), 0644)
}

// extractYAMLSection extracts a top-level YAML section from config content.
// Returns empty string if section not found. The key is matched only at column 0
// (a true top-level key), so an indented/nested 'profiles:' sub-key or one inside
// a quoted value or comment is not mistaken for the section.
func extractYAMLSection(config, sectionKey string) string {
	lines := strings.Split(config, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, sectionKey) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}

	var result strings.Builder
	for i := start; i < len(lines); i++ {
		line := lines[i]
		// Stop at the next top-level key (non-space, non-empty, non-comment).
		if i > start && len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
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

// SetupMockLM sets up a mock LM in the test environment and configures ctxloom to use it.
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
