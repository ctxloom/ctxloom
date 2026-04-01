// Remote type tests verify security warnings and content parsing for remote items.
// These are critical for user safety - remote content can execute code (MCP servers)
// or influence AI behavior (prompts/fragments), so users must see clear warnings.
package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: TestItemType_DirName and TestItemType_Plural are in browse_test.go

func TestItemType_DirName_CustomType(t *testing.T) {
	// Custom types should pluralize for directory naming consistency
	assert.Equal(t, "customs", ItemType("custom").DirName())
}

// =============================================================================
// Security Warning Tests
// =============================================================================
// Every remote content type must provide clear security warnings before installation.
// Users must understand the risks of running untrusted code or injecting untrusted prompts.

func TestRemoteMCPServer_SecurityWarning(t *testing.T) {
	// MCP servers execute arbitrary code - users must understand this risk
	server := RemoteMCPServer{
		Command: "test-command",
		Args:    []string{"arg1"},
	}

	warning := server.SecurityWarning()
	assert.Equal(t, "MCP SERVER INSTALLATION", warning.Title)
	assert.Contains(t, warning.Context, "execute commands")
	assert.GreaterOrEqual(t, len(warning.Risks), 3)
}

func TestRemoteMCPServer_Note(t *testing.T) {
	// Notes allow remote authors to communicate installation instructions
	server := RemoteMCPServer{
		NotesField:        "This is a test note",
		InstallationField: "Run: npm install test-server",
	}
	assert.Equal(t, "This is a test note", server.Note())
	assert.Equal(t, "Run: npm install test-server", server.Installation())
}

func TestRemoteContext_SecurityWarning(t *testing.T) {
	// Context fragments can contain prompt injection attacks - warn users
	ctx := RemoteContext{}

	warning := ctx.SecurityWarning()
	assert.Equal(t, "PROMPT INJECTION RISK", warning.Title)
	assert.Contains(t, warning.Context, "influence AI behavior")
	assert.GreaterOrEqual(t, len(warning.Risks), 3)
}

func TestRemoteContext_Note(t *testing.T) {
	ctx := RemoteContext{
		NotesField:        "Context note",
		InstallationField: "Add to your profile",
	}
	assert.Equal(t, "Context note", ctx.Note())
	assert.Equal(t, "Add to your profile", ctx.Installation())
}

func TestRemoteBundle_SecurityWarning(t *testing.T) {
	// Bundles combine multiple content types - warnings must reflect actual risk level
	t.Run("bundle without MCP", func(t *testing.T) {
		// Content-only bundles have prompt injection risk but no code execution
		bundle := RemoteBundle{
			Version:   "1.0",
			Fragments: map[string]RemoteBundleItem{"test": {Content: "content"}},
		}

		warning := bundle.SecurityWarning()
		assert.Equal(t, "BUNDLE INSTALLATION", warning.Title)
		assert.Contains(t, warning.Context, "AI context")
		assert.GreaterOrEqual(t, len(warning.Risks), 3)
	})

	t.Run("bundle with MCP", func(t *testing.T) {
		// MCP-containing bundles have elevated risk - code execution + prompt injection
		bundle := RemoteBundle{
			Version: "1.0",
			MCP:     &RemoteMCPServer{Command: "test-cmd"},
		}

		warning := bundle.SecurityWarning()
		assert.Equal(t, "BUNDLE INSTALLATION (WITH MCP SERVER)", warning.Title)
		assert.Contains(t, warning.Context, "executable code")
		assert.GreaterOrEqual(t, len(warning.Risks), 5) // Has more risks when MCP included
	})

	t.Run("bundle with empty MCP", func(t *testing.T) {
		// Empty MCP (no command) is effectively no MCP - don't scare users unnecessarily
		bundle := RemoteBundle{
			Version: "1.0",
			MCP:     &RemoteMCPServer{}, // Empty command
		}

		warning := bundle.SecurityWarning()
		assert.Equal(t, "BUNDLE INSTALLATION", warning.Title) // No MCP warning
	})
}

func TestRemoteBundle_Note(t *testing.T) {
	bundle := RemoteBundle{
		NotesField:        "Bundle note",
		InstallationField: "Run setup script",
	}
	assert.Equal(t, "Bundle note", bundle.Note())
	assert.Equal(t, "Run setup script", bundle.Installation())
}

func TestRemoteBundle_HasMCP(t *testing.T) {
	// HasMCP must correctly detect whether bundle includes executable code
	tests := []struct {
		name     string
		bundle   RemoteBundle
		expected bool
	}{
		{"no MCP", RemoteBundle{}, false},
		{"nil MCP", RemoteBundle{MCP: nil}, false},
		{"empty MCP command", RemoteBundle{MCP: &RemoteMCPServer{}}, false},
		{"valid MCP", RemoteBundle{MCP: &RemoteMCPServer{Command: "cmd"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.bundle.HasMCP())
		})
	}
}

// =============================================================================
// Content Parsing Tests
// =============================================================================
// ParseSecureContent must correctly deserialize remote YAML into typed structs,
// enabling the security warning system to analyze content before installation.

func TestParseSecureContent_Bundle(t *testing.T) {
	// Bundle parsing must extract all components for security analysis
	yaml := `
version: "1.0"
description: Test bundle
notes: Test note
mcp:
  command: test-cmd
  args: [arg1]
fragments:
  test-frag:
    content: Fragment content
    tags: [test]
prompts:
  test-prompt:
    content: Prompt content
`
	content, err := ParseSecureContent(ItemTypeBundle, []byte(yaml))
	require.NoError(t, err)

	bundle, ok := content.(RemoteBundle)
	require.True(t, ok)
	assert.Equal(t, "1.0", bundle.Version)
	assert.Equal(t, "Test bundle", bundle.Description)
	assert.Equal(t, "Test note", bundle.Note())
	assert.True(t, bundle.HasMCP())
	assert.Equal(t, "test-cmd", bundle.MCP.Command)
	assert.Len(t, bundle.Fragments, 1)
	assert.Len(t, bundle.Prompts, 1)
}

func TestParseSecureContent_Profile(t *testing.T) {
	// Profiles are parsed as context with potential prompt injection risk
	yaml := `
notes: Profile note
installation: Run setup first
`
	content, err := ParseSecureContent(ItemTypeProfile, []byte(yaml))
	require.NoError(t, err)

	ctx, ok := content.(RemoteContext)
	require.True(t, ok)
	assert.Equal(t, "Profile note", ctx.Note())
	assert.Equal(t, "Run setup first", ctx.Installation())
}

func TestParseSecureContent_DefaultType(t *testing.T) {
	// Unknown types default to context - safe fallback with prompt injection warning
	yaml := `notes: Context note`
	content, err := ParseSecureContent(ItemType("other"), []byte(yaml))
	require.NoError(t, err)

	ctx, ok := content.(RemoteContext)
	require.True(t, ok)
	assert.Equal(t, "Context note", ctx.Note())
}

func TestParseSecureContent_InvalidYAML(t *testing.T) {
	// Invalid YAML must fail parsing - never install corrupt content
	invalidYAML := `invalid: yaml: content: [[`
	_, err := ParseSecureContent(ItemTypeBundle, []byte(invalidYAML))
	require.Error(t, err)
}
