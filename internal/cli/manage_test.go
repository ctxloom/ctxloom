package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	// "init" is deliberately absent: the duplicate `manage init` entry point
	// was DELETED outright (not deprecated) — root `ctxloom init` is the sole
	// bootstrap (CLI-primary reorg plan, Decision 6). "config" stays as a
	// deprecated alias group (manageConfigCmd) even though its real home
	// promoted to the top-level `ctxloom config` (see
	// TestOldTopLevelPaths_AreRemoved for that assertion).
	for _, name := range []string{"install", "uninstall", "status", "hooks", "mcp", "statusline", "config", "gitignore"} {
		assert.NotNil(t, findSub(manage, name), "manage %s should exist", name)
	}
	assert.Nil(t, findSub(manage, "init"), "manage init was deleted; root `ctxloom init` is the sole bootstrap")
}

func TestManageMcp_SplitsRegistrationFromServerCRUD(t *testing.T) {
	mcp := findSub(findSub(rootCmd, "manage"), "mcp")
	require.NotNil(t, mcp)

	assert.NotNil(t, findSub(mcp, "install"), "manage mcp install toggles auto-register on")
	assert.NotNil(t, findSub(mcp, "uninstall"), "manage mcp uninstall toggles auto-register off")

	servers := findSub(mcp, "servers")
	require.NotNil(t, servers, "configured-server CRUD lives under manage mcp servers")
	assert.ElementsMatch(t, []string{"list", "add", "remove", "show"}, subNames(servers))
}

func TestManageHooks_HasInstallUninstallStatus(t *testing.T) {
	hooks := findSub(findSub(rootCmd, "manage"), "hooks")
	require.NotNil(t, hooks)
	assert.ElementsMatch(t, []string{"install", "uninstall", "status"}, subNames(hooks))
}

func TestOldTopLevelPaths_AreRemoved(t *testing.T) {
	// config is back at the top level (CLI-primary reorg plan, Decision 6:
	// `manage config *` promoted to `config *`); `manage config` stays working
	// as a deprecated alias group (TestManageNamespace_HasExpectedSubcommands).
	assert.NotNil(t, findSub(rootCmd, "config"), "config promoted to top level (plan Decision 6)")

	hook := findSub(rootCmd, "hook")
	require.NotNil(t, hook, "hook namespace stays (hidden callback home)")
	assert.Nil(t, findSub(hook, "apply"), "hook apply moved to manage hooks install")

	// The runtime mcp server stays top-level but sheds its CRUD verbs.
	mcp := findSub(rootCmd, "mcp")
	require.NotNil(t, mcp)
	assert.NotNil(t, findSub(mcp, "serve"), "mcp serve is the runtime entrypoint")
	assert.Nil(t, findSub(mcp, "auto-register"), "auto-register moved to manage mcp install/uninstall")
	assert.Nil(t, findSub(mcp, "add"), "mcp add moved to manage mcp servers add")
}

func TestInitAliasStaysTopLevel(t *testing.T) {
	// `manage init` was deleted (Decision 6); root `ctxloom init` is the sole,
	// canonical bootstrap entry point, not an alias for anything.
	assert.NotNil(t, findSub(rootCmd, "init"), "ctxloom init is the sole bootstrap entry point")
}

func TestCallbacksConsolidatedUnderHook(t *testing.T) {
	hook := findSub(rootCmd, "hook")
	require.NotNil(t, hook, "the hook namespace is the single home for machine callbacks")
	assert.True(t, hook.Hidden, "the callback namespace must be hidden")

	// All callbacks live under hook; the meta namespace is gone, and the
	// user-facing session/tasks namespaces no longer carry callbacks.
	assert.ElementsMatch(t,
		[]string{"inject-context", "hud", "session-bind", "stamp-plan"},
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
// command being re-parented under a visible namespace. Keep this list in sync
// with the callback paths baked into generated settings.
func TestCallbackCommandsAreHidden(t *testing.T) {
	callbacks := [][]string{
		{"hook", "inject-context"},
		{"hook", "hud"},
		{"hook", "session-bind"},
		{"hook", "stamp-plan"},
	}
	for _, path := range callbacks {
		c, _, err := rootCmd.Find(path)
		require.NoError(t, err, "callback %v must resolve", path)
		require.Equal(t, path[len(path)-1], c.Name(), "callback %v must resolve to the leaf", path)
		assert.True(t, c.Hidden, "callback %v must be hidden so it never overwhelms the user surface", path)
	}
}
