package support

import (
	"fmt"
	"os"
	"path/filepath"
)

// MockLM provides a fake language model for testing.
// It creates a shell script that echoes configurable responses.
type MockLM struct {
	// BinaryPath is the path to the mock LM script
	BinaryPath string

	// Response is what the mock LM will output
	Response string

	// ExitCode is what the mock LM will return
	ExitCode int

	// RecordedInput will contain the input received by the mock
	RecordedInputPath string
}

// NewMockLM creates a new mock LM in the given directory.
func NewMockLM(dir string) (*MockLM, error) {
	m := &MockLM{
		BinaryPath:        filepath.Join(dir, "mock-lm"),
		Response:          "Mock LM response",
		ExitCode:          0,
		RecordedInputPath: filepath.Join(dir, "mock-lm-input.txt"),
	}

	if err := m.Write(); err != nil {
		return nil, err
	}

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

// SetResponse sets the response and rewrites the script.
func (m *MockLM) SetResponse(response string) error {
	m.Response = response
	return m.Write()
}

// SetExitCode sets the exit code and rewrites the script.
func (m *MockLM) SetExitCode(code int) error {
	m.ExitCode = code
	return m.Write()
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
func (e *TestEnvironment) SetupMockLM() (*MockLM, error) {
	mockLM, err := NewMockLM(e.Root)
	if err != nil {
		return nil, err
	}

	// Create config that uses the mock LM by overriding claude-code's binary path
	// This way we use the existing claude-code plugin infrastructure but with our mock binary
	config := fmt.Sprintf(`lm:
  default_plugin: claude-code
  plugins:
    claude-code:
      binary_path: "%s"
defaults:
  use_distilled: false
personas: {}
`, mockLM.BinaryPath)

	// Write to project directory (takes precedence)
	if err := e.WriteFile(".scm/config.yaml", config); err != nil {
		return nil, err
	}

	return mockLM, nil
}
