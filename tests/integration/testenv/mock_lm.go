package testenv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	ctxloomconfig "github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// MockLM provides a fake language model for testing.
// Uses the built-in mock plugin for gRPC-based plugin system.
type MockLM struct {
	// Response is what the mock LM will output
	Response string

	// ExitCode is what the mock LM will return
	ExitCode int

	// echo leaves CTXLOOM_MOCK_RESPONSE out of the control map, so the
	// engine answers with its own default: the verbatim echo of what it
	// received. Response is kept for the SetResponse that follows.
	echo bool

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

// SetResponse sets the canned reply and rewrites the config with it.
func (m *MockLM) SetResponse(response string) error {
	m.Response = response
	m.echo = false
	return m.WriteConfig()
}

// Echo drops the canned reply and rewrites the config without it, so the
// engine answers with its own default — the verbatim echo of the mode,
// fragments, context and prompt it received (the mock backend's
// buildMockResponse). The knob has to be ABSENT for that: a set-but-empty
// value asks for an empty reply. This is how a scenario stands in a
// distiller that never compresses — the payload comes back as the answer.
func (m *MockLM) Echo() error {
	m.echo = true
	return m.WriteConfig()
}

// WriteConfig merges the mock plugin configuration into .ctxloom/config.yaml,
// touching only llm.configs.mock, llm.defaults.primary, config.use_distilled,
// and version — every other top-level key (agents, default_agent, workspace,
// any other engine's llm.configs entry, profiles, llm.defaults.fast, …) that
// was already on disk survives untouched. This used to
// rebuild the whole file from scratch, preserving only a hand-extracted
// profiles: section and silently destroying everything else a prior step
// (a journey Given, another engine's WriteConfig call) had written.
//
// The mock's CTXLOOM_MOCK_* knobs ride the entry's `mock_control` map — test
// control, which is fine in a project file. (The retired `env` key is not:
// it is refused at load, see config.RetiredLLMEnvKey.)
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
	// (internal/adapters/cli/run.go) — exactly what forced the F2 pty tests to carry
	// -y.
	upgrade.SetVersion(root, "version", ctxloomconfig.CurrentConfigVersion)

	llm := yamlx.EnsureMap(root, "llm")
	configs := yamlx.EnsureMap(llm, "configs")
	mockNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	yamlx.MapSet(mockNode, "type", yamlx.ScalarNode("mock"))
	// A headless run (--one-shot) declaring no headless-safe posture is
	// refused, and the mock's host default prompts; the scenarios drive
	// one-shots through this label, so it declares the posture they run at.
	yamlx.MapSet(mockNode, "permissions", yamlx.ScalarNode("bypass"))
	control := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	yamlx.MapSet(control, "CTXLOOM_MOCK_RECORD_FILE", quotedYAMLString(m.RecordedInputPath))
	if !m.echo {
		yamlx.MapSet(control, "CTXLOOM_MOCK_RESPONSE", quotedYAMLString(m.Response))
	}
	yamlx.MapSet(control, "CTXLOOM_MOCK_EXIT_CODE", quotedYAMLString(fmt.Sprint(m.ExitCode)))
	yamlx.MapSet(mockNode, "mock_control", control)
	// Only the mock entry is touched — any other engine's llm.configs entry
	// survives untouched (that survival is the whole point of the fix).
	yamlx.MapSet(configs, "mock", mockNode)

	defaults := yamlx.EnsureMap(llm, "defaults")
	yamlx.MapSet(defaults, "primary", yamlx.ScalarNode("mock"))

	cfgSection := yamlx.EnsureMap(root, "config")
	useDistilled := yamlx.ScalarNode("false")
	useDistilled.Tag = "!!bool"
	yamlx.MapSet(cfgSection, "use_distilled", useDistilled)

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
// EnsureHomeMockLabel merges an `llm.configs.<label>: {type: mock}` entry into
// the isolated HOME's config.yaml (the machine layer), leaving everything
// else in that file as it was. It exists for a fixture that must launch a
// mock-engine session in a project whose OWN config names some other engine
// as primary — the session owner a delegation scenario stands up (its
// project config is the scenario's, and the live tiers make a real engine
// primary there). The label rides the machine layer because no scenario
// fixture rewrites that file, so it survives a later rewrite of the
// project's config.
func (e *TestEnvironment) EnsureHomeMockLabel(label string) error {
	return e.mergeHomeConfig(func(root *yaml.Node) {
		configs := yamlx.EnsureMap(yamlx.EnsureMap(root, "llm"), "configs")
		mockNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlx.MapSet(mockNode, "type", yamlx.ScalarNode("mock"))
		yamlx.MapSet(configs, label, mockNode)
	})
}

// mergeHomeConfig applies edit to the isolated HOME's config.yaml document,
// creating it (at the current version) when absent and leaving everything
// else in the file as it was.
func (e *TestEnvironment) mergeHomeConfig(edit func(root *yaml.Node)) error {
	configPath := filepath.Join(e.HomeDir, ".ctxloom", "config.yaml")
	var doc yaml.Node
	if data, err := os.ReadFile(configPath); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
			return fmt.Errorf("parse existing home config.yaml: %w", uerr)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	upgrade.SetVersion(root, "version", ctxloomconfig.CurrentConfigVersion)
	edit(root)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("marshal home config.yaml: %w", err)
	}
	_ = enc.Close()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(configPath, buf.Bytes(), 0o644)
}

func quotedYAMLString(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}

// ErrMockNeverInvoked is returned by GetRecordedInput when the record file
// does not exist -- this used to collapse into ("", nil), making a
// mock that was NEVER invoked indistinguishable from one invoked with empty
// input, and any negative assertion over the returned string ("does not
// contain X") would pass vacuously against either. Callers that want the old
// tolerant behavior (a fresh fixture, before any run) can check for this
// sentinel explicitly and treat it as "".
var ErrMockNeverInvoked = errors.New("mock was never invoked: no recorded input file exists")

// GetRecordedInput returns the input that was sent to the mock LM.
func (m *MockLM) GetRecordedInput() (string, error) {
	data, err := os.ReadFile(m.RecordedInputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrMockNeverInvoked
		}
		return "", err
	}
	return string(data), nil
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
