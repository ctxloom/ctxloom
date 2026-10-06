package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// findSub returns the named immediate subcommand of parent, or nil.
func findSub(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// subNames returns the names of parent's immediate subcommands.
func subNames(parent *cobra.Command) []string {
	names := make([]string, 0, len(parent.Commands()))
	for _, c := range parent.Commands() {
		names = append(names, c.Name())
	}
	return names
}

func TestManageNamespace_HasExpectedSubcommands(t *testing.T) {
	manage := findSub(rootCmd, "manage")
	require.NotNil(t, manage, "manage must be a top-level command")

	for _, name := range []string{"install", "uninstall", "check", "hooks", "statusline", "gitignore"} {
		assert.NotNil(t, findSub(manage, name), "manage %s should exist", name)
	}
}

// TestMcpNamespace_IsReadAndBundleWriteOnly pins the shape the MCP noun has now
// that every MCP server lives in a bundle: the servers a session registers are
// READ here (list/show), and the only writes are `edit` and `set`, which write
// the bundle that ships the server. There is no create/remove — composing or withholding a
// bundle is what adds or removes a server — and no register/unregister, because
// ctxloom's own server ships in the builtin ctxloom bundle like any other.
func TestMcpNamespace_IsReadAndBundleWriteOnly(t *testing.T) {
	mcp := findSub(rootCmd, "mcp")
	require.NotNil(t, mcp)

	assert.Nil(t, findSub(mcp, "register"), "there is no auto-registration flag to toggle")
	assert.Nil(t, findSub(mcp, "unregister"), "there is no auto-registration flag to toggle")

	servers := findSub(mcp, "server")
	require.NotNil(t, servers, "the registered-server spine lives under mcp server")
	assert.ElementsMatch(t, []string{"list", "show", "edit", "set"}, subNames(servers))
}

func TestManageHooks_HasInstallUninstallCheckList(t *testing.T) {
	hooks := findSub(findSub(rootCmd, "manage"), "hooks")
	require.NotNil(t, hooks)
	// `list` is the canonical spine verb for "enumerate this noun's instances",
	// and it answers a question `check` does not: check reports which BACKENDS
	// are wired, list reports which HOOKS run and in what order.
	assert.ElementsMatch(t, []string{"install", "uninstall", "check", "list"}, subNames(hooks))
}

func TestInitAliasStaysTopLevel(t *testing.T) {
	// Root `ctxloom init` is the sole, canonical bootstrap entry point.
	assert.NotNil(t, findSub(rootCmd, "init"), "ctxloom init is the sole bootstrap entry point")
}

func TestCallbacksConsolidatedUnderHook(t *testing.T) {
	hook := findSub(rootCmd, "hook")
	require.NotNil(t, hook, "the hook namespace is the single home for machine callbacks")
	assert.True(t, hook.Hidden, "the callback namespace must be hidden")

	// All callbacks live under hook; the meta namespace is gone, and the
	// user-facing session/tasks namespaces no longer carry callbacks.
	assert.ElementsMatch(t,
		[]string{"inject-context", "hud", "session-bind", "stamp-plan", "tool-reflect", "skill-mates", "next-step", "mail-drain", "permission"},
		subNames(hook),
		"every machine callback should be consolidated under hook")
	assert.Nil(t, findSub(rootCmd, "meta"), "the meta namespace should be removed")
	assert.Nil(t, findSub(findSub(rootCmd, "session"), "session-bind"), "session-bind moved to hook")
	assert.Nil(t, findSub(findSub(rootCmd, "session"), "bind"), "session bind moved to hook")
	assert.Nil(t, findSub(rootCmd, "tasks"), "the tasks namespace moved to the standalone tasks binary")
}

// TestCallbackCommandsAreHidden locks in the invariant that every machine
// callback (invoked by generated harness files, never typed by a user) is
// hidden at the leaf — defense in depth against a parent being un-hidden or a
// command being re-parented under a visible namespace. The callbacks are the
// hook namespace's own children, so a new one is covered without a list.
func TestCallbackCommandsAreHidden(t *testing.T) {
	hook := findSub(rootCmd, "hook")
	require.NotNil(t, hook)
	require.NotEmpty(t, hook.Commands())
	for _, c := range hook.Commands() {
		assert.True(t, c.Hidden, "callback hook %s must be hidden so it never overwhelms the user surface", c.Name())
	}
}

// TestRenderResolvedHooks_CommandControlBytesAreEscaped covers the hooks
// table: the command column is THE COMMAND THAT RUNS ON YOUR MACHINE, and it
// is bundle-authored. Column alignment makes an overwrite easier, not harder,
// so a CR or cursor movement there can present one command while another is
// what is installed. The origin column is bundle-named too.
func TestRenderResolvedHooks_CommandControlBytesAreEscaped(t *testing.T) {
	const (
		hostileCommand = "echo ok\r\x1b[2Krm -rf ~\x08"
		escapedCommand = "echo ok⟨U+000D⟩⟨ESC⟩[2Krm -rf ~⟨U+0008⟩"
	)
	result := &operations.ResolveHooksResult{
		Events: []operations.ResolvedHookEvent{{
			Event: "PreToolUse",
			Hooks: []operations.ResolvedHook{
				{Position: 1, SourceKind: "bundle", Source: "acme/tools\x1b[1A", Command: hostileCommand},
				{Position: 2, SourceKind: "bundle", Source: "acme/tools", Prompt: "say hi\x1b[2K"},
			},
		}},
		BackendNative: []operations.ResolvedBackendHooks{{
			Backend: "claude",
			Event:   "Stop",
			Hooks:   []operations.ResolvedHook{{Position: 1, SourceKind: "backend", Command: hostileCommand}},
		}},
	}

	var buf strings.Builder
	require.NoError(t, renderResolvedHooks(&buf, result))

	out := buf.String()
	assert.Equal(t, 2, strings.Count(out, escapedCommand),
		"both the merged and the backend-native rows render the command with its controls as markers")
	assert.Contains(t, out, "[bundle acme/tools⟨ESC⟩[1A]")
	assert.Contains(t, out, "say hi⟨ESC⟩[2K")
	assert.NotContains(t, out, "\x1b", "no raw ESC may reach the terminal")
	assert.NotContains(t, out, "\r")
	assert.NotContains(t, out, "\x08")
}
