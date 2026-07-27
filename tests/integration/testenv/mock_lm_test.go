package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMockLM_WriteConfig_PreservesUnrelatedSections pins U159-F01/U163-F02:
// WriteConfig used to rebuild .ctxloom/config.yaml from scratch, preserving
// only a top-level profiles: section — silently destroying agents:,
// default_agent:, workspace:, and any other engine's llm.configs entry that
// an earlier step (a journey Given, a prior WriteConfig call for a different
// engine) had written. It must instead merge: touch only llm.configs.mock,
// llm.defaults.primary, config.use_distilled, and version.
func TestMockLM_WriteConfig_PreservesUnrelatedSections(t *testing.T) {
	projectDir := t.TempDir()
	configDir := filepath.Join(projectDir, ".ctxloom")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	existing := `version: 6
llm:
  configs:
    claude:
      type: claude-code
  defaults:
    primary: claude
    fast: claude
agents:
  reviewer:
    profiles: [dev]
default_agent: reviewer
workspace: worktree
profiles:
  dev:
    fragments: [foo]
`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &MockLM{
		Response:          "hello",
		ExitCode:          0,
		RecordedInputPath: filepath.Join(projectDir, "mock-lm-input.txt"),
		ProjectDir:        projectDir,
	}
	if err := m.WriteConfig(); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)

	for _, want := range []string{
		"agents:",
		"reviewer:",
		"default_agent: reviewer",
		"workspace: worktree",
		"claude:",
		"type: claude-code",
		"fast: claude",
		"fragments:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteConfig destroyed unrelated config; missing %q from output:\n%s", want, out)
		}
	}

	// And the mock's own settings must still land.
	for _, want := range []string{"mock:", "type: mock", "primary: mock"} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteConfig did not write its own mock settings; missing %q from output:\n%s", want, out)
		}
	}
}

// TestMockLM_WriteConfig_FreshFile pins the no-existing-config path: it must
// still produce a loadable config with the mock wired up as primary, exactly
// as SetupMockLM's first call relies on.
func TestMockLM_WriteConfig_FreshFile(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ctxloom"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &MockLM{
		Response:          "hi",
		ExitCode:          0,
		RecordedInputPath: filepath.Join(projectDir, "mock-lm-input.txt"),
		ProjectDir:        projectDir,
	}
	if err := m.WriteConfig(); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(projectDir, ".ctxloom", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, want := range []string{"mock:", "type: mock", "primary: mock", "version:"} {
		if !strings.Contains(out, want) {
			t.Errorf("fresh WriteConfig missing %q from output:\n%s", want, out)
		}
	}
}
