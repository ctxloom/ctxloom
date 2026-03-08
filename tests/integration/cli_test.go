package integration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SophisticatedContextManager/scm/tests/integration/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpRequestID tracks the current request ID for MCP calls
var mcpRequestID = 0

func buildMCPRequest(method string, params string) string {
	mcpRequestID++
	if params == "" {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"%s"}`, mcpRequestID, method)
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"%s","params":%s}`, mcpRequestID, method, params)
}

func setupTestEnv(t *testing.T) *testenv.TestEnvironment {
	t.Helper()
	env, err := testenv.NewTestEnvironment()
	require.NoError(t, err, "failed to create test environment")
	require.NoError(t, env.Setup(), "failed to setup test environment")
	require.NoError(t, env.InitGitRepo(), "failed to init git repo")
	require.NoError(t, env.CreateProjectSCM(), "failed to create .scm directory")
	t.Cleanup(func() { _ = env.Cleanup() })
	return env
}

// writeFragment writes a fragment to the local.yaml bundle file.
// This appends to the bundle, creating it if it doesn't exist.
func writeFragment(t *testing.T, env *testenv.TestEnvironment, name string, tags []string, content string) {
	t.Helper()

	// Read existing bundle if present
	bundlePath := ".scm/bundles/local.yaml"
	existing, _ := env.ReadFile(bundlePath)

	// Build new bundle content
	var bundle strings.Builder
	if existing == "" {
		bundle.WriteString("version: \"1.0\"\n")
		bundle.WriteString("fragments:\n")
	} else {
		// Remove trailing newline and add to it
		bundle.WriteString(strings.TrimSuffix(existing, "\n"))
		bundle.WriteString("\n")
	}

	// Add the new fragment
	bundle.WriteString(fmt.Sprintf("  %s:\n", name))
	if len(tags) > 0 {
		bundle.WriteString("    tags:\n")
		for _, tag := range tags {
			bundle.WriteString(fmt.Sprintf("      - %s\n", tag))
		}
	}
	bundle.WriteString("    content: |\n")
	for _, line := range strings.Split(content, "\n") {
		bundle.WriteString(fmt.Sprintf("      %s\n", line))
	}

	require.NoError(t, env.WriteFile(bundlePath, bundle.String()), "failed to write fragment")
}

func writeProfile(t *testing.T, env *testenv.TestEnvironment, name, content string) {
	t.Helper()
	path := fmt.Sprintf(".scm/profiles/%s.yaml", name)
	require.NoError(t, env.WriteFile(path, content), "failed to write profile")
}

func writePrompt(t *testing.T, env *testenv.TestEnvironment, name, content string) {
	t.Helper()
	path := fmt.Sprintf(".scm/prompts/%s.md", name)
	require.NoError(t, env.WriteFile(path, content), "failed to write prompt")
}

// =============================================================================
// Version Command
// =============================================================================

func TestVersion(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.RunSCM("version")

	assert.Equal(t, 0, env.LastExitCode())
	// Version outputs just the version string like "v0.0.12-abc123"
	assert.Regexp(t, `v\d+\.\d+\.\d+`, env.LastOutput())
}

// =============================================================================
// Completion Command
// =============================================================================

func TestCompletion(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "powershell"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			env := setupTestEnv(t)

			_ = env.RunSCM("completion", shell)

			assert.Equal(t, 0, env.LastExitCode())
			assert.NotEmpty(t, env.LastOutput())
		})
	}
}

// =============================================================================
// Run Command - Critical Paths
// =============================================================================

func TestRun_SingleFragment(t *testing.T) {
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("LM response."))

	writeFragment(t, env, "test-fragment", []string{"testing"}, "This is test content.")

	_ = env.RunSCM("run", "-f", "test-fragment", "--print", "test prompt")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "This is test content")
	assert.Contains(t, recorded, "test prompt")
}

func TestRun_MultipleFragments(t *testing.T) {
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("Response."))

	writeFragment(t, env, "frag-one", []string{"first"}, "Content from fragment one.")
	writeFragment(t, env, "frag-two", []string{"second"}, "Content from fragment two.")

	_ = env.RunSCM("run", "-f", "frag-one", "-f", "frag-two", "--print", "combined test")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "Content from fragment one")
	assert.Contains(t, recorded, "Content from fragment two")
}

func TestRun_WithTags(t *testing.T) {
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("OK"))

	writeFragment(t, env, "security-frag", []string{"security"}, "Security guidelines here.")
	writeFragment(t, env, "style-frag", []string{"style"}, "Style guidelines here.")

	_ = env.RunSCM("run", "-t", "security", "--print", "tag test")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "Security guidelines")
	assert.NotContains(t, recorded, "Style guidelines")
}

func TestRun_WithProfile(t *testing.T) {
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("OK"))

	writeFragment(t, env, "profile-frag", []string{"profile"}, "Profile fragment content.")
	writeProfile(t, env, "test-profile", `name: test-profile
description: Test profile
bundles:
  - local#fragments/profile-frag
`)

	_ = env.RunSCM("run", "-p", "test-profile", "--print", "profile test")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "Profile fragment content")
}

func TestRun_VariableSubstitution(t *testing.T) {
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("OK"))

	writeFragment(t, env, "var-frag", []string{"variables"}, "The language is {{language}}.\nThe version is {{version}}.")
	writeProfile(t, env, "var-profile", `name: var-profile
description: Variable profile
bundles:
  - local#fragments/var-frag
variables:
  language: Go
  version: "1.21"
`)

	_ = env.RunSCM("run", "-p", "var-profile", "--print", "var test")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "The language is Go")
	assert.Contains(t, recorded, "The version is 1.21")
}

func TestRun_DryRun(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "dry-frag", []string{"dry"}, "Dry run content.")

	_ = env.RunSCM("run", "-f", "dry-frag", "--dry-run", "test prompt")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "Dry run content")
}

func TestRun_NonexistentFragment(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.RunSCM("run", "-f", "nonexistent", "--print", "test")

	assert.Equal(t, 1, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "not found")
}

func TestRun_NonexistentProfile(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.RunSCM("run", "-p", "nonexistent", "--print", "test")

	assert.Equal(t, 1, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "unknown profile")
}

func TestRun_SubdirectoryFragment(t *testing.T) {
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("OK"))

	// Create a bundle in subdirectory
	bundleContent := `version: "1.0"
fragments:
  golang:
    tags:
      - golang
    content: |
      Go coding guidelines from subdirectory.
`
	require.NoError(t, env.WriteFile(".scm/bundles/lang.yaml", bundleContent))

	_ = env.RunSCM("run", "-f", "lang#fragments/golang", "--print", "test")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "Go coding guidelines from subdirectory")
}

// =============================================================================
// MCP Command - Critical Paths
// =============================================================================

func TestMCP_Initialize(t *testing.T) {
	env := setupTestEnv(t)

	req := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	_ = env.RunSCMWithStdin(req+"\n", "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "protocolVersion")
	assert.Contains(t, env.LastOutput(), "scm")
}

func TestMCP_ToolsList(t *testing.T) {
	env := setupTestEnv(t)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	listReq := buildMCPRequest("tools/list", "")
	input := initReq + "\n" + listReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "list_fragments")
	assert.Contains(t, output, "get_fragment")
	assert.Contains(t, output, "list_profiles")
	assert.Contains(t, output, "get_profile")
	assert.Contains(t, output, "assemble_context")
}

func TestMCP_ListFragments(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "test-frag", []string{"testing"}, "Test content.")

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"list_fragments","arguments":{}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "test-frag")
	assert.Contains(t, output, "testing")
}

func TestMCP_GetFragment(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "get-test", []string{"demo"}, "This is the fragment content.")

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"get_fragment","arguments":{"name":"local#fragments/get-test"}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "This is the fragment content")
	assert.Contains(t, output, "demo")
}

func TestMCP_GetFragment_Nonexistent(t *testing.T) {
	env := setupTestEnv(t)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"get_fragment","arguments":{"name":"nonexistent"}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "not found")
}

func TestMCP_AssembleContext_WithFragment(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "assemble-frag", []string{"assemble"}, "Assembled fragment content.")

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"assemble_context","arguments":{"fragments":["local#fragments/assemble-frag"]}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "Assembled fragment content")
}

func TestMCP_AssembleContext_WithProfile(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "profile-frag", []string{"profile"}, "Profile fragment content.")
	writeProfile(t, env, "test-profile", `name: test-profile
description: Test
bundles:
  - local#fragments/profile-frag
`)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"assemble_context","arguments":{"profile":"test-profile"}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "Profile fragment content")
}

func TestMCP_AssembleContext_WithVariables(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "var-frag", []string{"vars"}, "The language is {{language}}.")
	writeProfile(t, env, "var-profile", `name: var-profile
description: Variables
bundles:
  - local#fragments/var-frag
variables:
  language: Python
`)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"assemble_context","arguments":{"profile":"var-profile"}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "The language is Python")
}

func TestMCP_UnknownTool(t *testing.T) {
	env := setupTestEnv(t)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"unknown_tool","arguments":{}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunSCMWithStdin(input, "mcp")

	assert.Equal(t, 0, env.LastExitCode())
	output := strings.ToLower(env.LastOutput())
	assert.Contains(t, output, "unknown tool")
}

// =============================================================================
// Profile Command - Critical Paths
// =============================================================================

func TestProfile_List_Empty(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.RunSCM("profile", "list")

	assert.Equal(t, 0, env.LastExitCode())
}

func TestProfile_List_WithProfiles(t *testing.T) {
	env := setupTestEnv(t)

	writeProfile(t, env, "test-profile", `name: test-profile
description: A test profile
bundles: []
`)

	_ = env.RunSCM("profile", "list")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "test-profile")
}

func TestProfile_Show(t *testing.T) {
	env := setupTestEnv(t)

	writeProfile(t, env, "detailed", `name: detailed
description: A detailed profile
bundles:
  - frag-one
  - frag-two
variables:
  language: Go
`)

	_ = env.RunSCM("profile", "show", "detailed")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "detailed")
	assert.Contains(t, output, "frag-one")
}

func TestProfile_Show_Nonexistent(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.RunSCM("profile", "show", "nonexistent")

	assert.NotEqual(t, 0, env.LastExitCode())
}
