package resources

import (
	"strings"
	"testing"
)

func TestListBuiltinCommands(t *testing.T) {
	names, err := ListBuiltinCommands()
	if err != nil {
		t.Fatalf("ListBuiltinCommands: %v", err)
	}

	// Should have at least the discover command
	found := false
	for _, name := range names {
		if name == "discover" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 'discover' command, got: %v", names)
	}
}

func TestGetBuiltinCommand(t *testing.T) {
	content, err := GetBuiltinCommand("discover")
	if err != nil {
		t.Fatalf("GetBuiltinCommand: %v", err)
	}

	// Should contain frontmatter with description
	if !strings.Contains(string(content), "description:") {
		t.Error("Expected description in frontmatter")
	}

	// Should contain the main content
	if !strings.Contains(string(content), "Scan the current project") {
		t.Error("Expected discover prompt content")
	}
}

func TestGetBuiltinCommand_Unknown(t *testing.T) {
	_, err := GetBuiltinCommand("nope-no-such-command")
	if err == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
}

func TestListBuiltinCommands_OnlyMdFiles(t *testing.T) {
	names, err := ListBuiltinCommands()
	if err != nil {
		t.Fatalf("ListBuiltinCommands: %v", err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".md") {
			t.Errorf("ListBuiltinCommands must strip the .md extension; got %q", n)
		}
	}
}

func TestGetConfigSchema(t *testing.T) {
	data, err := GetConfigSchema()
	if err != nil {
		t.Fatalf("GetConfigSchema: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("config schema must be non-empty")
	}
	// Must be valid JSON shape — accept either '{' or whitespace+'{'.
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		t.Errorf("config schema should look like JSON; got prefix %q", string(data[:32]))
	}
	// Should reference the standard JSON Schema draft URI.
	if !strings.Contains(string(data), "json-schema.org") {
		t.Error("config schema should reference json-schema.org")
	}
}

func TestGetExampleConfig(t *testing.T) {
	data, err := GetExampleConfig()
	if err != nil {
		t.Fatalf("GetExampleConfig: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("example config must be non-empty")
	}
}

func TestGetDefaultRemotes(t *testing.T) {
	data, err := GetDefaultRemotes()
	if err != nil {
		t.Fatalf("GetDefaultRemotes: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("default remotes must be non-empty")
	}
	// Sanity: should be YAML, should mention at least the ctxloom-default
	// remote name.
	if !strings.Contains(string(data), "ctxloom-default") {
		t.Error("default-remotes.yaml should declare the ctxloom-default remote")
	}
}

func TestGetBuiltinBundle(t *testing.T) {
	data, err := GetBuiltinBundle("tasks")
	if err != nil {
		t.Fatalf("GetBuiltinBundle(tasks): %v", err)
	}
	if len(data) == 0 {
		t.Fatal("tasks bundle must be non-empty")
	}
	// Sanity-check the shape — bundle YAML always has a version key.
	if !strings.Contains(string(data), "version:") {
		t.Error("tasks bundle should declare a version")
	}
	// And it should ship its two namesake hooks.
	if !strings.Contains(string(data), "tasks capture") {
		t.Error("tasks bundle should ship the capture hook")
	}
}

func TestGetBuiltinBundle_Unknown(t *testing.T) {
	_, err := GetBuiltinBundle("no-such-bundle")
	if err == nil {
		t.Fatal("expected error for unknown bundle, got nil")
	}
}

func TestListBuiltinBundles(t *testing.T) {
	names, err := ListBuiltinBundles()
	if err != nil {
		t.Fatalf("ListBuiltinBundles: %v", err)
	}
	// Must include the tasks bundle ctxloom embeds.
	found := false
	for _, n := range names {
		if n == "tasks" {
			found = true
		}
		if strings.HasSuffix(n, ".yaml") {
			t.Errorf("ListBuiltinBundles must strip the .yaml extension; got %q", n)
		}
	}
	if !found {
		t.Errorf("expected 'tasks' bundle in list, got %v", names)
	}
}
