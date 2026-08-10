package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// runCLIJSON executes rootCmd with args plus "--format json" and returns the
// decoded payload, requiring both a clean exit and valid JSON. This is the
// format-debt paydown's unit-level proof for the installer/mcp-registration
// surface (manage.go, mcp.go): --format json must actually produce JSON, not
// the human text that these RunEs used to print via bare fmt.Printf
// regardless of the flag.
func runCLIJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append(append([]string{}, args...), "--format", "json"))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	require.NoError(t, rootCmd.Execute(), "ctxloom %v --format json", args)
	require.Truef(t, json.Valid(out.Bytes()), "invalid JSON for %v:\n%s", args, out.String())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	return payload
}

// TestManageGitignoreInstall_FormatJSON pins the `manage gitignore install`
// half of the installer surface: it must route through emit() instead of a
// bare fmt.Printf, so --format json renders {"status":"updated","path":...}
// rather than silently discarding the flag.
func TestManageGitignoreInstall_FormatJSON(t *testing.T) {
	testsupport.ProjectDir(t)
	payload := runCLIJSON(t, "manage", "gitignore", "install")
	require.Equal(t, "updated", payload["status"])
	require.NotEmpty(t, payload["path"])
}

// TestManageGitignoreInstall_WriteFailureIsNotSilentSuccess is a
// regression pin: `manage gitignore install` used to print "Updated <path>"
// and exit 0 even when the .gitignore write failed. Here the write is forced
// to fail by making ".gitignore" a directory (so gitignore.Ensure's
// os.ReadFile hits a non-IsNotExist error) — the command must return an
// error, not report success.
func TestManageGitignoreInstall_WriteFailureIsNotSilentSuccess(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".gitignore"), 0o755))

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"manage", "gitignore", "install"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	require.Error(t, err, "a failed .gitignore write must not report success")
	require.NotContains(t, out.String(), "Updated", "must not print the success line when the write failed")
}

// TestMcpRegisterUnregister_FormatJSON pins setMcpAutoRegister (behind
// `mcp register`/`mcp unregister`): --format json must render the
// auto-registration result instead of the two bare fmt.Printf/Println lines
// it used to be stuck with regardless of --format.
func TestMcpRegisterUnregister_FormatJSON(t *testing.T) {
	testsupport.ProjectDir(t)

	payload := runCLIJSON(t, "mcp", "register")
	require.Equal(t, "enabled", payload["status"])
	require.Equal(t, true, payload["auto_register"])

	payload = runCLIJSON(t, "mcp", "unregister")
	require.Equal(t, "disabled", payload["status"])
	require.Equal(t, false, payload["auto_register"])
}

// TestManageStatusline_FormatJSON pins setStatusline (shared by `manage
// statusline install`/`uninstall`): --format json must render the resulting
// state instead of silently discarding it.
func TestManageStatusline_FormatJSON(t *testing.T) {
	testsupport.ProjectDir(t)

	payload := runCLIJSON(t, "manage", "statusline", "install")
	require.Equal(t, "enabled", payload["status"])
	require.Equal(t, true, payload["statusline"])

	payload = runCLIJSON(t, "manage", "statusline", "uninstall")
	require.Equal(t, "disabled", payload["status"])
	require.Equal(t, false, payload["statusline"])
}

// TestMcpServerCreateDelete_FormatJSON pins runMCPAdd/runMCPRemove (behind
// `mcp server create`/`remove`): --format json must render the operations
// result struct instead of the bare fmt.Printf lines both used
// unconditionally.
//
// Uses the default unified target (mcp.servers.*): mcp.servers.*.command is
// ScopeShared (internal/config/layerscope, a deliberate divergence from the
// design doc's literal ScopeMachine — see policy_default.go's own comment:
// command is REQUIRED by the mcpServer schema, so Machine-scoping it would
// make mcp.servers.* impossible to populate with a working entry from the
// project layer at all), so a unified server created in the committed
// PROJECT file this test writes to survives the reload `mcp server remove
// --yes` performs internally. No --backend workaround needed.
//
// `remove` here is run twice: bare (--yes-less), asserting the preview
// payload and that the server SURVIVES it, then with --yes, asserting the
// applied payload and that the server is actually GONE — the two directions
// of the safety posture, not just that the command exits 0.
func TestMcpServerCreateDelete_FormatJSON(t *testing.T) {
	testsupport.ProjectDir(t)

	payload := runCLIJSON(t, "mcp", "server", "create", "coverage-server", "--command", "echo")
	require.Equal(t, "coverage-server", payload["name"])
	require.Equal(t, "echo", payload["command"])

	preview := runCLIJSON(t, "mcp", "server", "remove", "coverage-server")
	require.Equal(t, "preview", preview["status"])
	require.Equal(t, false, preview["applied"])
	require.True(t, mcpServerListedByName(t, "coverage-server"),
		"the bare (no --yes) path must leave the server configured")

	payload = runCLIJSON(t, "mcp", "server", "remove", "coverage-server", "--yes")
	require.Equal(t, "coverage-server", payload["name"])
	require.False(t, mcpServerListedByName(t, "coverage-server"), "--yes must actually remove the server")
}

// mcpServerListedByName reports whether `mcp server list --format json`
// currently lists a server by that name.
func mcpServerListedByName(t *testing.T, name string) bool {
	t.Helper()
	payload := runCLIJSON(t, "mcp", "server", "list")
	servers, _ := payload["servers"].([]any)
	for _, s := range servers {
		row, _ := s.(map[string]any)
		if row["name"] == name {
			return true
		}
	}
	return false
}

// TestManageInstallUninstall_FormatJSON pins runManageInstall/
// runManageUninstall end to end, including the --print dry-run branch of
// `manage install`.
func TestManageInstallUninstall_FormatJSON(t *testing.T) {
	testsupport.ProjectDir(t)

	plan := runCLIJSON(t, "manage", "install", "--print")
	require.Equal(t, false, plan["ctxloom_dir_exists"])
	require.NotEmpty(t, plan["ctxloom_dir"])

	// --print from the previous call sticks on the shared package-level
	// manageInstallPrint var (cobra doesn't reset an unpassed bool flag to
	// its default across repeated Execute() calls on the same command tree)
	// — pass it explicitly so this call takes the real install branch.
	installed := runCLIJSON(t, "manage", "install", "--print=false")
	require.Equal(t, true, installed["initialized"])
	require.NotEmpty(t, installed["status"])

	removed := runCLIJSON(t, "manage", "uninstall")
	require.NotEmpty(t, removed["status"])
	require.NotEmpty(t, removed["note"])
}

// TestManageHooksInstallUninstall_FormatJSON pins `manage hooks
// install`/`uninstall`'s own inline RunEs (distinct from `manage
// install`/`uninstall`'s top-level orchestrators, but the same emit() shape).
func TestManageHooksInstallUninstall_FormatJSON(t *testing.T) {
	testsupport.ProjectDir(t)

	installed := runCLIJSON(t, "manage", "hooks", "install")
	require.NotEmpty(t, installed["status"])
	require.NotEmpty(t, installed["project_root"])

	removed := runCLIJSON(t, "manage", "hooks", "uninstall")
	require.NotEmpty(t, removed["status"])
}
