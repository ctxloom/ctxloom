package resources

import (
	"reflect"
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

func TestGetBuiltinCommand_Recover(t *testing.T) {
	// /recover ships as a built-in so every ctxloom project gets it, not just
	// the ctxloom repo's own .claude/commands. It wraps the recover_session
	// MCP tool.
	content, err := GetBuiltinCommand("recover")
	if err != nil {
		t.Fatalf("GetBuiltinCommand(recover): %v", err)
	}
	if !strings.Contains(string(content), "description:") {
		t.Error("Expected description in frontmatter")
	}
	if !strings.Contains(string(content), "recover_session") {
		t.Error("recover command should drive the recover_session MCP tool")
	}
}

func TestListBuiltinCommands_IncludesRecover(t *testing.T) {
	names, err := ListBuiltinCommands()
	if err != nil {
		t.Fatalf("ListBuiltinCommands: %v", err)
	}
	for _, name := range names {
		if name == "recover" {
			return
		}
	}
	t.Errorf("expected 'recover' built-in command, got: %v", names)
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
	// A remote carries NO trust flag anymore (signature-envelope spec §11):
	// trust is keyed to the publisher key, not the remote. The deleted
	// `trust_bundles` flag must not reappear — its presence would silently
	// restore the hash-blind source bypass this rework removed.
	if strings.Contains(string(data), "trust_bundles") {
		t.Error("default-remotes.yaml must NOT carry a trust_bundles flag — source trust is deleted")
	}
}

// TestGetBuiltinBundle_TaskloomDeleted proves taskloom is no longer an
// embedded builtin bundle (signature-envelope spec §4.3, S8): its content
// (fragment, hooks, MCP server) now ships from its own binary's loadout
// (`taskloom loadout --format json`, cmd/taskloom/loadout.yaml), discovered
// on PATH — see internal/config's ProbeCompanionLoadouts and
// TestResolveBundleMCPServers_IncludesCompanionLoadoutServers_Gated. This
// replaces the old TestGetBuiltinBundle, which asserted the OPPOSITE
// (taskloom.yaml present and non-empty) — that fixture is gone by design.
func TestGetBuiltinBundle_TaskloomDeleted(t *testing.T) {
	_, err := GetBuiltinBundle("taskloom")
	if err == nil {
		t.Fatal("taskloom.yaml must no longer be an embedded builtin bundle — it ships from its own loadout now")
	}
}

func TestGetBuiltinBundle_Unknown(t *testing.T) {
	_, err := GetBuiltinBundle("no-such-bundle")
	if err == nil {
		t.Fatal("expected error for unknown bundle, got nil")
	}
}

// TestListBuiltinBundles_OnlyIsolation pins the current embedded set. S8
// deleted the last two embedded bundles (ltk, taskloom) — their content now
// ships from the companions' own loadouts — leaving resources/builtin_bundles/
// with only a README (see its doc comment) until "isolation" became the first
// bundle that genuinely needs to ship compiled into the binary itself (no
// companion of its own). ListBuiltinBundles must report exactly that one name,
// and must still strip the .yaml extension for whatever it finds.
func TestListBuiltinBundles_OnlyIsolation(t *testing.T) {
	names, err := ListBuiltinBundles()
	if err != nil {
		t.Fatalf("ListBuiltinBundles: %v", err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".yaml") {
			t.Errorf("ListBuiltinBundles must strip the .yaml extension; got %q", n)
		}
	}
	if want := []string{"isolation"}; !reflect.DeepEqual(names, want) {
		t.Errorf("expected embedded builtin bundles %v, got %v", want, names)
	}
}

// TestGetPromptText verifies every embedded prompt template loads non-empty,
// trims its trailing newline, and that a missing name errors. These prompts
// back package-level vars via MustGetPromptText, so a renamed or unembedded
// file would otherwise only surface as an init-time panic at runtime.
func TestGetPromptText(t *testing.T) {
	for _, name := range []string{
		"profile-discovery",
		"agent-setup",
		"tooling",
		"distill-default",
		"mcp-server-instructions",
		"session-distill",
		"session-distill-reduce",
	} {
		got, err := GetPromptText(name)
		if err != nil {
			t.Errorf("GetPromptText(%q): %v", name, err)
			continue
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("GetPromptText(%q) is empty", name)
		}
		if strings.HasSuffix(got, "\n") {
			t.Errorf("GetPromptText(%q) has a trailing newline; want it trimmed", name)
		}
	}

	if _, err := GetPromptText("does-not-exist"); err == nil {
		t.Error("GetPromptText(missing) returned nil error")
	}
}

// TestSetupPrompts_ContentContract pins the tokens the collapsed init
// interview depends on: the agent-setup prompt must lead with the standard
// trio and teach the write surface (`agent set`, `--isolation`), and the
// profile-discovery prompt must bridge into agent setup rather than ending
// the conversation after profiles. A drift here silently breaks the merged
// discovery session (internal/cli.discoverySessionPrompt) without any
// compile-time signal.
func TestSetupPrompts_ContentContract(t *testing.T) {
	setup, err := GetPromptText("agent-setup")
	if err != nil {
		t.Fatalf("GetPromptText(agent-setup): %v", err)
	}
	for _, want := range []string{
		"coordinator",
		"developer",
		"finder",
		"--runtime container",
		"--workspace worktree",
		"ctxloom container check",
		"ctxloom agent set",
		"ctxloom agent list",
	} {
		if !strings.Contains(setup, want) {
			t.Errorf("agent-setup prompt lost required token %q", want)
		}
	}

	tooling, err := GetPromptText("tooling")
	if err != nil {
		t.Fatalf("GetPromptText(tooling): %v", err)
	}
	for _, want := range []string{
		"ctxloom container scaffold",
		"ctxloom container build",
		"explicit approval",
		"Never apply tooling automatically",
	} {
		if !strings.Contains(tooling, want) {
			t.Errorf("tooling prompt lost required token %q", want)
		}
	}

	discovery, err := GetPromptText("profile-discovery")
	if err != nil {
		t.Fatalf("GetPromptText(profile-discovery): %v", err)
	}
	for _, want := range []string{
		"agent setup",
		"one continuous setup interview",
	} {
		if !strings.Contains(discovery, want) {
			t.Errorf("profile-discovery prompt lost the agent-setup bridge token %q", want)
		}
	}
}
