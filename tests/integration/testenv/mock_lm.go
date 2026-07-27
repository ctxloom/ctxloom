package testenv

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	ctxloomconfig "github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
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

// WriteConfig merges the mock plugin configuration into .ctxloom/config.yaml,
// touching only llm.configs.mock, llm.defaults.primary, config.use_distilled,
// and version — every other top-level key (agents, default_agent, workspace,
// any other engine's llm.configs entry, profiles, llm.defaults.fast, …) that
// was already on disk survives untouched. Before U159-F01/U163-F02 this
// rebuilt the whole file from scratch, preserving only a hand-extracted
// profiles: section and silently destroying everything else a prior step
// (a journey Given, another engine's WriteConfig call) had written.
func (m *MockLM) WriteConfig() error {
	if m.ProjectDir == "" {
		return fmt.Errorf("ProjectDir not set; call SetupMockLM first")
	}

	configPath := filepath.Join(m.ProjectDir, ".ctxloom", "config.yaml")

	var doc yaml.Node
	if data, err := os.ReadFile(configPath); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
			return fmt.Errorf("parse existing config.yaml: %w", uerr)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]

	// Pinned to ctxloomconfig.CurrentConfigVersion rather than a hardcoded
	// number so this fixture can never itself fall behind the schema again:
	// a stale version here would make loading apply an in-memory upgrade,
	// which on a real pty (both stdin and stdout a tty) fires the
	// interactive "rewrite to the current format?" confirmUpgrade prompt
	// (internal/cli/run.go) — exactly what forced the F2 pty tests to carry
	// -y.
	upgrade.SetVersion(root, "version", ctxloomconfig.CurrentConfigVersion)

	llm := upgrade.EnsureMap(root, "llm")
	configs := upgrade.EnsureMap(llm, "configs")
	mockNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	upgrade.MapSet(mockNode, "type", upgrade.ScalarNode("mock"))
	env := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	upgrade.MapSet(env, "CTXLOOM_MOCK_RECORD_FILE", quotedYAMLString(m.RecordedInputPath))
	upgrade.MapSet(env, "CTXLOOM_MOCK_RESPONSE", quotedYAMLString(m.Response))
	upgrade.MapSet(env, "CTXLOOM_MOCK_EXIT_CODE", quotedYAMLString(fmt.Sprint(m.ExitCode)))
	upgrade.MapSet(mockNode, "env", env)
	// Only the mock entry is touched — any other engine's llm.configs entry
	// survives untouched (that survival is the whole point of the fix).
	upgrade.MapSet(configs, "mock", mockNode)

	defaults := upgrade.EnsureMap(llm, "defaults")
	upgrade.MapSet(defaults, "primary", upgrade.ScalarNode("mock"))

	cfgSection := upgrade.EnsureMap(root, "config")
	useDistilled := upgrade.ScalarNode("false")
	useDistilled.Tag = "!!bool"
	upgrade.MapSet(cfgSection, "use_distilled", useDistilled)

	// profiles: left exactly as found if present; created empty only for a
	// brand new file so downstream readers see a valid (if empty) section.
	upgrade.EnsureMap(root, "profiles")

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("marshal config.yaml: %w", err)
	}
	_ = enc.Close()

	return os.WriteFile(configPath, buf.Bytes(), 0644)
}

// quotedYAMLString builds a double-quoted string scalar node, letting the
// yaml library own the escaping rather than hand-rolling it (the old
// escapeYAMLString covered only backslash/quote/newline).
func quotedYAMLString(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
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
