//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
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
	require.NoError(t, env.CreateProjectConfig(), "failed to create .ctxloom directory")
	// Cleanup's error used to be discarded at every call site,
	// which is the "reports nothing, removes nothing" blindness
	// TestEnvironment.forceRemoveAll's own doc names as the mechanism behind
	// an observed /tmp leak. assert.NoError (not require) since this runs in
	// a t.Cleanup func, after the test body has already finished.
	t.Cleanup(func() { assert.NoError(t, env.Cleanup(), "test environment cleanup") })
	return env
}

// writeFragment writes a fragment to the local.yaml bundle file.
// This appends to the bundle, creating it if it doesn't exist.
func writeFragment(t *testing.T, env *testenv.TestEnvironment, name string, tags []string, content string) {
	t.Helper()

	// Read existing bundle if present
	bundlePath := ".ctxloom/content/bundles/local.yaml"
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
	fmt.Fprintf(&bundle, "  %s:\n", name)
	if len(tags) > 0 {
		bundle.WriteString("    tags:\n")
		for _, tag := range tags {
			fmt.Fprintf(&bundle, "      - %s\n", tag)
		}
	}
	bundle.WriteString("    content: |\n")
	for _, line := range strings.Split(content, "\n") {
		fmt.Fprintf(&bundle, "      %s\n", line)
	}

	require.NoError(t, env.WriteFile(bundlePath, bundle.String()), "failed to write fragment")
}

func writeProfile(t *testing.T, env *testenv.TestEnvironment, name, content string) {
	t.Helper()
	path := fmt.Sprintf(".ctxloom/profiles/%s.yaml", name)
	require.NoError(t, env.WriteFile(path, content), "failed to write profile")
}

// =============================================================================
// Version Command
// =============================================================================

func TestVersion(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("version")

	assert.Equal(t, 0, env.LastExitCode())
	// Version outputs version string like "v0.0.12-abc123" or "dev" in CI
	output := strings.TrimSpace(env.LastOutput())
	assert.True(t, output == "dev" || regexp.MustCompile(`v\d+\.\d+\.\d+`).MatchString(output),
		"Expected version to be 'dev' or match semver pattern, got: %s", output)
}

// =============================================================================
// Completion Command
// =============================================================================

func TestCompletion(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "powershell"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			env := setupTestEnv(t)

			_ = env.Run("completion", shell)

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

	_ = env.Run("run", "-f", "test-fragment", "--one-shot", "test prompt")

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

	_ = env.Run("run", "-f", "frag-one", "-f", "frag-two", "--one-shot", "combined test")

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

	_ = env.Run("run", "-t", "security", "--one-shot", "tag test")

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

	_ = env.Run("run", "-p", "test-profile", "--one-shot", "profile test")

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

	_ = env.Run("run", "-p", "var-profile", "--one-shot", "var test")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "The language is Go")
	assert.Contains(t, recorded, "The version is 1.21")
}

func TestRun_DryRun(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "dry-frag", []string{"dry"}, "Dry run content.")

	_ = env.Run("run", "-f", "dry-frag", "--dry-run", "test prompt")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "Dry run content")
}

// TestRun_Agent_DryRun drives the --agent arm end-to-end: `agent create`
// writes the binding (engine defaulted, runtime declared), and `run --agent X
// --dry-run` previews THAT binding — its composed profile context, and the
// session axes header (workspace from the invocation, runtime from the agent).
//
// It used to drive `agent set`, a verb the verb-spine reorg replaced with the
// create/edit pair. That left the suite RED rather than falsely green only by
// luck: `--profiles` is not a flag on the bare `agent` group node the unknown
// verb fell through to, so cobra failed the flag parse. Had the invocation
// carried no flags it would have printed `agent`'s help and exited 0, exactly
// like TestConfig_Show/TestConfig_Get below.
//
// It also used to drive `--runtime container` — the spelling `container` was
// renamed to `container-rootless` when the ownership axis shipped, and this
// test carried the stale spelling because `agent create` used to WARN that
// "container" was unknown and then persist it anyway (a reproduced defect:
// the requested container isolation silently degraded to host, and the
// written config then failed schema validation at the next real `ctxloom
// run`). Now `agent create`/`edit` REFUSE an unknown runtime outright — see
// TestRun_AgentCreate_RejectsUnknownRuntime below for that direction — so this
// test exercises the dry-run preview with one of the three legal values.
func TestRun_Agent_DryRun(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "agent-frag", []string{"agent"}, "Agent-composed content.")
	writeProfile(t, env, "agent-profile", `name: agent-profile
description: Agent profile
bundles:
  - local#fragments/agent-frag
`)

	_ = env.Run("agent", "create", "dev", "--profiles", "agent-profile", "--runtime", "container-rootless")
	require.Equal(t, 0, env.LastExitCode(), "agent create: %s", env.LastOutput())
	require.Contains(t, env.LastOutput(), "dev",
		"agent create must report the binding it wrote, not print the agent group's help")

	_ = env.Run("run", "--agent", "dev", "--dry-run", "agent test")

	assert.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	// The preview is read as DATA. Off a terminal the default is now
	// machine-readable, and this is the only test of the --agent arm's preview
	// on the wire at all — run_characterization_test.go's dryRunJSON cases all
	// drive the profile arm. Reading the fields also bites harder than the
	// prose did: the axes are pinned by equality rather than by a substring
	// that "runtime: container-rootless-something" would also satisfy.
	var got struct {
		Agent     string   `json:"agent"`
		Workspace string   `json:"workspace"`
		Runtime   string   `json:"runtime"`
		Profiles  []string `json:"profiles"`
		Context   string   `json:"context"`
	}
	out := env.LastStdout()
	require.NoError(t, json.Unmarshal([]byte(out), &got),
		"`run --agent --dry-run` must emit its preview as a JSON document off a terminal:\n"+out)

	assert.Equal(t, "dev", got.Agent, "the preview names the binding it resolved")
	assert.Equal(t, "container-rootless", got.Runtime, "the agent's declared runtime axis surfaces")
	// The two axes are independent and only the DECLARED one is reported: the
	// invocation set no --workspace, so that field stays empty in the same
	// payload that carries a runtime. A preview that filled it in would be
	// inventing an isolation guarantee nobody asked for.
	assert.Empty(t, got.Workspace, "an unset session workspace is reported as unset, not defaulted:\n"+out)
	assert.Equal(t, []string{"agent-profile"}, got.Profiles, "the agent's profile set scopes the preview")
	assert.Contains(t, got.Context, "Agent-composed content", "the agent's composed profile context is previewed")
}

// TestRun_AgentCreate_RejectsUnknownRuntime pins the fix for the defect
// TestRun_Agent_DryRun's doc above describes: `agent create dev --runtime
// container` must exit non-zero AND write nothing — no config.yaml at all
// (setupTestEnv's CreateProjectConfig scaffolds the .ctxloom directory but
// never writes the file itself; SetAgent's Manager.Update, which is what
// would create it, never opens because validateAgentAxes refuses first), let
// alone an agents.dev entry carrying the invalid value. Asserting only the
// exit code (or only the error text) would pass even if the old
// warn-and-store behavior were still there underneath a bolted-on non-zero
// exit; checking the file itself is what actually proves the refusal writes
// nothing, per this project's characteristic bug (a report that doesn't
// match what got written, in either direction).
func TestRun_AgentCreate_RejectsUnknownRuntime(t *testing.T) {
	env := setupTestEnv(t)

	require.False(t, env.FileExists(".ctxloom/config.yaml"),
		"precondition: nothing has written config.yaml yet")

	_ = env.Run("agent", "create", "dev", "--runtime", "container")

	assert.Equal(t, 1, env.LastExitCode(), "an unknown runtime must be refused: %s", env.LastOutput())
	out := strings.ToLower(env.LastOutput())
	assert.Contains(t, out, "unknown runtime")
	assert.Contains(t, out, "host")
	assert.Contains(t, out, "container-rootless")
	assert.Contains(t, out, "container-rootful")

	assert.False(t, env.FileExists(".ctxloom/config.yaml"),
		"a refused agent create must write NOTHING — not even the file itself")

	// And the effect a report-only assertion would miss: the agent genuinely
	// does not exist afterward, not merely "the command said it didn't".
	_ = env.Run("agent", "show", "dev")
	assert.Equal(t, 1, env.LastExitCode(), "a refused create must leave no agent to show: %s", env.LastOutput())
}

// TestRun_Agent_Unknown pins the hard-error contract: an explicit --agent
// naming a binding that doesn't exist fails the run (user intent — unlike
// acp's editor-serving degrade).
func TestRun_Agent_Unknown(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("run", "--agent", "nosuch", "--dry-run", "x")

	assert.Equal(t, 1, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "not found")
}

// TestRun_Agent_ExclusiveWithProfile: --agent fully determines context, so
// combining it with -p is a flag error, not a silent precedence pick.
func TestRun_Agent_ExclusiveWithProfile(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("run", "--agent", "dev", "-p", "some-profile", "--dry-run", "x")

	assert.Equal(t, 1, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "none of the others can be")
}

func TestRun_NonexistentFragment(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("run", "-f", "nonexistent", "--one-shot", "test")

	assert.Equal(t, 1, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "not found")
}

func TestRun_NonexistentProfile(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("run", "-p", "nonexistent", "--one-shot", "test")

	assert.Equal(t, 1, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "not found")
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
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/lang.yaml", bundleContent))

	_ = env.Run("run", "-f", "lang#fragments/golang", "--one-shot", "test")

	assert.Equal(t, 0, env.LastExitCode())
	recorded, _ := mockLM.GetRecordedInput()
	assert.Contains(t, recorded, "Go coding guidelines from subdirectory")
}

// TestRun_NonTTYStdinNeverEngagesTerminalUI makes explicit what every other
// TestRun_* case in this file already proves implicitly (Run's stdin is
// unset, so os/exec gives the child /dev/null, never a tty): a non-tty stdin
// skips the whole terminal observation layer (surround bar + prefix-key
// viewer). Unlike those cases, this one omits --one-shot, so the run attempts
// INTERACTIVE mode (pb.ExecutionMode_INTERACTIVE) rather than short-circuiting
// to ONESHOT before ever reaching run_terminal.go's interactiveTerminal —
// proving the term.IsTerminal(stdin) gate itself, which --one-shot bypasses
// entirely, correctly declines to wrap a non-tty stdin.
func TestRun_NonTTYStdinNeverEngagesTerminalUI(t *testing.T) {
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("plain response"))

	writeFragment(t, env, "non-tty-fragment", nil, "non-tty test content")

	_ = env.Run("run", "-f", "non-tty-fragment", "hello non-tty")

	assert.Equal(t, 0, env.LastExitCode())
	out := env.LastOutput()
	assert.NotContains(t, out, "\x1b[1;", "a non-tty stdin never establishes the surround's protected region")
	assert.NotContains(t, out, "\x1b7\x1b[r", "a non-tty stdin never wires the prefix interceptor")
	assert.Contains(t, out, "plain response", "the run still completes normally on the unwrapped seams")
}

// =============================================================================
// MCP Command - Critical Paths
// =============================================================================

func TestMCP_Initialize(t *testing.T) {
	env := setupTestEnv(t)

	req := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	_ = env.RunWithStdin(req+"\n", "mcp", "serve")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "protocolVersion")
	assert.Contains(t, env.LastOutput(), "ctxloom")
}

func TestMCP_ToolsList(t *testing.T) {
	env := setupTestEnv(t)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	listReq := buildMCPRequest("tools/list", "")
	input := initReq + "\n" + listReq + "\n"
	_ = env.RunWithStdin(input, "mcp", "serve")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	// The MCP surface is retrieval only: read-only listings moved to resources
	// (ctxloom://...), management (bundles, remotes, hooks) moved to the CLI,
	// and task tracking moved to the standalone tasks binary's MCP server.
	// Assert on tools that remain on the surface.
	assert.Contains(t, output, "assemble_context")
	assert.Contains(t, output, "search_content")
	assert.Contains(t, output, "search_library")
}

func TestMCP_ListFragments(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "test-frag", []string{"testing"}, "Test content.")

	// list_fragments is now the ctxloom://fragments resource, not a tool.
	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	readReq := buildMCPRequest("resources/read", `{"uri":"ctxloom://fragments"}`)
	input := initReq + "\n" + readReq + "\n"
	_ = env.RunWithStdin(input, "mcp", "serve")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "test-frag")
	assert.Contains(t, output, "testing")
}

func TestMCP_GetFragment(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "get-test", []string{"demo"}, "This is the fragment content.")

	// get_fragment is now the ctxloom://fragments/{name} resource (bare name),
	// which returns the fragment's content (markdown), not its tags.
	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	readReq := buildMCPRequest("resources/read", `{"uri":"ctxloom://fragments/get-test"}`)
	input := initReq + "\n" + readReq + "\n"
	_ = env.RunWithStdin(input, "mcp", "serve")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "This is the fragment content")
}

func TestMCP_GetFragment_Nonexistent(t *testing.T) {
	env := setupTestEnv(t)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	readReq := buildMCPRequest("resources/read", `{"uri":"ctxloom://fragments/nonexistent"}`)
	input := initReq + "\n" + readReq + "\n"
	_ = env.RunWithStdin(input, "mcp", "serve")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, strings.ToLower(env.LastOutput()), "not found")
}

func TestMCP_AssembleContext_WithFragment(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "assemble-frag", []string{"assemble"}, "Assembled fragment content.")

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	// assemble_context's fragment list is keyed "bundles" over the wire.
	callParams := `{"name":"assemble_context","arguments":{"bundles":["local#fragments/assemble-frag"]}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunWithStdin(input, "mcp", "serve")

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
	_ = env.RunWithStdin(input, "mcp", "serve")

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
	_ = env.RunWithStdin(input, "mcp", "serve")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "The language is Python")
}

func TestMCP_UnknownTool(t *testing.T) {
	env := setupTestEnv(t)

	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := `{"name":"unknown_tool","arguments":{}}`
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = env.RunWithStdin(input, "mcp", "serve")

	assert.Equal(t, 0, env.LastExitCode())
	output := strings.ToLower(env.LastOutput())
	assert.Contains(t, output, "unknown tool")
}

// =============================================================================
// Profile Command - Critical Paths
// =============================================================================

func TestProfile_List_Empty(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("profile", "list")

	assert.Equal(t, 0, env.LastExitCode())
}

func TestProfile_List_WithProfiles(t *testing.T) {
	env := setupTestEnv(t)

	writeProfile(t, env, "test-profile", `name: test-profile
description: A test profile
bundles: []
`)

	_ = env.Run("profile", "list")

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

	_ = env.Run("profile", "show", "detailed")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "detailed")
	assert.Contains(t, output, "frag-one")
}

func TestProfile_Show_Nonexistent(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("profile", "show", "nonexistent")

	assert.NotEqual(t, 0, env.LastExitCode())
}

// =============================================================================
// Bundle Command - Critical Paths
// =============================================================================

func TestBundle_List_Empty(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("bundle", "list")

	assert.Equal(t, 0, env.LastExitCode())
}

func TestBundle_List_WithBundles(t *testing.T) {
	env := setupTestEnv(t)

	// Create a bundle with a fragment
	bundleContent := `version: "1.0"
fragments:
  test-frag:
    tags:
      - test
    content: |
      Test content
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/test-bundle.yaml", bundleContent))

	_ = env.Run("bundle", "list")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "test-bundle")
}

func TestBundle_Show(t *testing.T) {
	env := setupTestEnv(t)

	bundleContent := `version: "1.0"
fragments:
  frag1:
    tags:
      - tag1
    content: |
      Content 1
  frag2:
    tags:
      - tag2
    content: |
      Content 2
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/show-test.yaml", bundleContent))

	_ = env.Run("bundle", "show", "show-test")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "frag1")
	assert.Contains(t, output, "frag2")
}

func TestBundle_Show_Nonexistent(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("bundle", "show", "nonexistent")

	assert.NotEqual(t, 0, env.LastExitCode())
}

func TestBundle_Create(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("bundle", "create", "my-bundle")

	assert.Equal(t, 0, env.LastExitCode())

	// Verify bundle file was created
	content, err := env.ReadFile(".ctxloom/content/bundles/my-bundle.yaml")
	require.NoError(t, err)
	assert.Contains(t, content, "version:")
	assert.Contains(t, content, "fragments:")
}

func TestBundle_Create_WithDescription(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("bundle", "create", "desc-bundle", "--description", "A test bundle")

	assert.Equal(t, 0, env.LastExitCode())

	content, err := env.ReadFile(".ctxloom/content/bundles/desc-bundle.yaml")
	require.NoError(t, err)
	assert.Contains(t, content, "description: A test bundle")
}

func TestBundle_PromptList(t *testing.T) {
	env := setupTestEnv(t)

	bundleContent := `version: "1.0"
commands:
  prompt1:
    content: |
      Prompt content 1
  prompt2:
    content: |
      Prompt content 2
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/prompt-bundle.yaml", bundleContent))

	// `bundle show` renders the bundle's Prompts section; the former
	// `bundle prompt list` subtree was removed (see cmd/bundle.go).
	_ = env.Run("bundle", "show", "prompt-bundle")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "prompt1")
	assert.Contains(t, output, "prompt2")
}

func TestBundle_View(t *testing.T) {
	env := setupTestEnv(t)

	bundleContent := `version: "1.0"
fragments:
  display-frag:
    content: |
      This is the content to display
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/view-test.yaml", bundleContent))

	_ = env.Run("bundle", "view", "view-test#fragments/display-frag")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "This is the content to display")
}

func TestBundle_Export(t *testing.T) {
	env := setupTestEnv(t)

	bundleContent := `version: "1.0"
fragments:
  export-frag:
    tags:
      - export
    content: |
      Export content
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/export-test.yaml", bundleContent))

	_ = env.Run("bundle", "export", "export-test", "-o", "exported.tar.gz")

	assert.Equal(t, 0, env.LastExitCode())
}

// =============================================================================
// Fragment Command - Critical Paths
// =============================================================================

func TestFragment_List_Empty(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("fragment", "list")

	assert.Equal(t, 0, env.LastExitCode())
}

func TestFragment_List_WithFragments(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "frag1", []string{"tag1"}, "Content 1")
	writeFragment(t, env, "frag2", []string{"tag2"}, "Content 2")

	_ = env.Run("fragment", "list")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "frag1")
	assert.Contains(t, output, "frag2")
}

func TestFragment_Show(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "show-frag", []string{"test"}, "Test content for show")

	_ = env.Run("fragment", "show", "local#fragments/show-frag")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "show-frag")
	assert.Contains(t, output, "Test content for show")
}

func TestFragment_Show_Nonexistent(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("fragment", "show", "nonexistent")

	assert.NotEqual(t, 0, env.LastExitCode())
}

func TestFragment_Create(t *testing.T) {
	env := setupTestEnv(t)

	// First create the local bundle by writing a fragment
	writeFragment(t, env, "setup-frag", []string{}, "Setup content")

	// Now create a new fragment in the local bundle
	_ = env.Run("fragment", "create", "local", "new-frag")

	assert.Equal(t, 0, env.LastExitCode())

	// Verify fragment was created in local bundle
	_ = env.Run("fragment", "show", "local#fragments/new-frag")
	assert.Equal(t, 0, env.LastExitCode())
	// Fragment is created with placeholder content
	assert.Contains(t, env.LastOutput(), "new-frag")
}

func TestFragment_Create_WithTags(t *testing.T) {
	env := setupTestEnv(t)

	// Create the local bundle first
	writeFragment(t, env, "setup-frag", []string{"setup"}, "Setup content")

	// Create a new fragment (tags are set in the bundle YAML, not via create command)
	_ = env.Run("fragment", "create", "local", "tagged-frag")

	assert.Equal(t, 0, env.LastExitCode())

	// Verify fragment was created
	_ = env.Run("fragment", "show", "local#fragments/tagged-frag")
	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "tagged-frag")
}

// TestFragment_Search and TestFragment_Search_ByTag lived here and drove
// `ctxloom fragment search` — a command that has never existed. Searching is
// the top-level `ctxloom search`, and its two arms are already covered, by the
// same fixtures and the same assertions, in TestSearch_Fragments and
// TestSearch_WithTags below. Repointing these would have produced two verbatim
// duplicates of those, so they are gone rather than restated.
//
// They were RED, not falsely green: the `-t` arm died on an unknown shorthand
// and the plain arm on a payload assertion the help text could not satisfy.
// That is only because both happened to assert on OUTPUT — the neighbouring
// exit-code-only tests in this file survived the identical mistake silently
// for as long.

// =============================================================================
// Prompt Command - Critical Paths
// =============================================================================

func TestPrompt_List_Empty(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("command", "list")

	assert.Equal(t, 0, env.LastExitCode())
}

func TestPrompt_List_WithPrompts(t *testing.T) {
	env := setupTestEnv(t)

	bundleContent := `version: "1.0"
commands:
  analyze:
    content: |
      Analyze the following:
  summarize:
    content: |
      Summarize the following:
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/prompts.yaml", bundleContent))

	_ = env.Run("command", "list")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "analyze")
	assert.Contains(t, output, "summarize")
}

func TestPrompt_Show(t *testing.T) {
	env := setupTestEnv(t)

	bundleContent := `version: "1.0"
commands:
  test-prompt:
    content: |
      This is a test prompt with detailed instructions.
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/prompt-test.yaml", bundleContent))

	_ = env.Run("command", "show", "prompt-test#commands/test-prompt")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "test prompt with detailed instructions")
}

// =============================================================================
// Search Command - Critical Paths
// =============================================================================

func TestSearch_Fragments(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "redis-cache", []string{"cache"}, "Redis caching strategies and best practices")
	writeFragment(t, env, "memcached-guide", []string{"cache"}, "Memcached setup and tuning")

	_ = env.Run("search", "cache")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "redis-cache")
	assert.Contains(t, output, "memcached-guide")
}

func TestSearch_Prompts(t *testing.T) {
	env := setupTestEnv(t)

	bundleContent := `version: "1.0"
commands:
  code-review:
    content: |
      Review this code for quality, performance and security
  document:
    content: |
      Generate documentation for this code
`
	require.NoError(t, env.WriteFile(".ctxloom/content/bundles/search-prompts.yaml", bundleContent))

	_ = env.Run("search", "code")

	assert.Equal(t, 0, env.LastExitCode())
	assert.Contains(t, env.LastOutput(), "code-review")
}

func TestSearch_WithTags(t *testing.T) {
	env := setupTestEnv(t)

	writeFragment(t, env, "go-concurrency", []string{"golang", "concurrency"}, "Go concurrency patterns")
	writeFragment(t, env, "go-generics", []string{"golang", "generics"}, "Go generics guide")
	writeFragment(t, env, "rust-concurrency", []string{"rust", "concurrency"}, "Rust async/await")

	_ = env.Run("search", "-t", "concurrency")

	assert.Equal(t, 0, env.LastExitCode())
	output := env.LastOutput()
	assert.Contains(t, output, "go-concurrency")
	assert.Contains(t, output, "rust-concurrency")
	assert.NotContains(t, output, "go-generics")
}

// =============================================================================
// Init Command
// =============================================================================

func TestInit_CreatesProjectStructure(t *testing.T) {
	env, err := testenv.NewTestEnvironment()
	require.NoError(t, err)
	require.NoError(t, env.Setup())
	t.Cleanup(func() { assert.NoError(t, env.Cleanup(), "test environment cleanup") })

	_ = env.Run("init")

	assert.Equal(t, 0, env.LastExitCode())

	// Verify directory structure was created
	assert.True(t, env.FileExists(".ctxloom"), "Expected .ctxloom directory to exist")
	assert.True(t, env.FileExists(".ctxloom/config.yaml"), "Expected .ctxloom/config.yaml to exist")
}

// TestInit_GitMissing_FailsLoudBeforeClone pins PRIME's targeted
// system-dependency gate (init-as-skill slice ④, coordinator addendum): a
// machine with no `git` on PATH must fail init loud, naming git, BEFORE the
// remote-clone step ever runs — never a raw, unguided git error out of the
// clone machinery, and never a silent partial success. `--non-interactive`
// still runs this gate (a scripted init still needs git to clone).
func TestInit_GitMissing_FailsLoudBeforeClone(t *testing.T) {
	env, err := testenv.NewTestEnvironment()
	require.NoError(t, err)
	require.NoError(t, env.Setup())
	t.Cleanup(func() { assert.NoError(t, env.Cleanup(), "test environment cleanup") })

	// The ctxloom binary itself is invoked by its absolute path (testenv.Run
	// execs env.AppBinary directly), so it needs nothing on PATH to start;
	// only checkSystemDeps' own exec.LookPath probes see this empty PATH.
	env.SetEnv("PATH", t.TempDir())

	_ = env.Run("init", "--non-interactive")

	assert.NotEqual(t, 0, env.LastExitCode(), "init must fail loud (nonzero exit) when git is missing")
	assert.Contains(t, env.LastOutput(), "git", "the failure must name git")

	// PRIME reached the marker dir/config write (the dep gate runs right
	// after it) ...
	assert.True(t, env.FileExists(".ctxloom/config.yaml"), "marker dir/config is written before the dep gate")
	// ... but never reached the clone step the dep gate sits in front of: no
	// remote was ever cloned into the repo cache.
	assert.False(t, env.FileExists(".ctxloom/cache/repos"), "no remote should have been cloned once the git gate failed")
}

// =============================================================================
// Config Command
// =============================================================================

// writeConfig writes a .ctxloom/config.yaml carrying values distinctive enough
// that finding them in a command's output proves that command READ THIS FILE —
// not merely that it exited 0 with something plausible on stdout.
func writeConfig(t *testing.T, env *testenv.TestEnvironment) {
	t.Helper()
	require.NoError(t, env.WriteFile(".ctxloom/config.yaml", `version: 6
llm:
  configs:
    payload-probe-engine:
      type: claude-code
      model: payload-probe-model
  defaults:
    primary: payload-probe-engine
config:
  use_distilled: true
`), "write config.yaml")
}

// TestConfig_Show and TestConfig_Get assert on the PAYLOAD, not the exit code.
//
// Both used to drive `manage config show` / `manage config get llm`. There is
// no `manage config` subtree — configuration lives at the top-level `config`
// command — so cobra printed `manage`'s help and exited 0, and an
// exit-code-only assertion cannot tell that apart from the command having run.
// They passed for their whole life while exercising nothing. The exit code is
// still checked, but it is the weakest of the assertions here on purpose: the
// value written into config.yaml is what proves the command did the work.
func TestConfig_Show(t *testing.T) {
	env := setupTestEnv(t)
	writeConfig(t, env)

	_ = env.Run("config", "show")

	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	out := env.LastOutput()
	assert.Contains(t, out, "payload-probe-engine", "config show renders the configured llm label")
	assert.Contains(t, out, "payload-probe-model", "config show renders the configured model")
	assert.Contains(t, out, "use_distilled", "config show renders the `config` section too, not just llm")
	assert.NotContains(t, out, "Usage:", "a cobra help dump is the false-green this test exists to catch")
}

func TestConfig_Get(t *testing.T) {
	env := setupTestEnv(t)
	writeConfig(t, env)

	// Sections are config/llm/mcp/profiles in schema v3 (the old "defaults"
	// section was folded into "config").
	_ = env.Run("config", "get", "llm")

	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	out := env.LastOutput()
	assert.Contains(t, out, "payload-probe-engine", "config get llm renders the llm section")
	assert.Contains(t, out, "payload-probe-model")
	// Section scoping is the whole point of `get` over `show`: a sibling
	// section's keys must NOT come along.
	assert.NotContains(t, out, "use_distilled", "config get llm is scoped to the llm section")
	assert.NotContains(t, out, "Usage:", "a cobra help dump is the false-green this test exists to catch")
}

// TestConfig_ManageSubtreeIsGone pins the product half of the same defect: the
// removed `manage config` path must FAIL, naming the typo, rather than
// printing manage's help and exiting 0. Any unknown subcommand of any group
// node behaves this way — see TestCLI_UnknownSubcommandFails.
func TestConfig_ManageSubtreeIsGone(t *testing.T) {
	env := setupTestEnv(t)

	_ = env.Run("manage", "config", "show")

	assert.NotEqual(t, 0, env.LastExitCode(),
		"a removed subcommand must fail loudly, not print help and exit 0: %s", env.LastOutput())
	assert.Contains(t, env.LastOutput(), "config", "the error names the subcommand that does not exist")
}
