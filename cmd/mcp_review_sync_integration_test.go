package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// seedFileRemote creates a bare git repo (served over file://) containing a
// ctxloom/v1 bundle that ships an MCP server and a hook, and returns its
// file:// URL. The cached fetcher clones file:// repos like any other remote
// (see ADR 0015), so this drives a real sync without the network.
func seedFileRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	git := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	git(root, "init", "--bare", "-b", "main", bare)
	git(root, "init", "-b", "main", work)
	git(work, "config", "user.email", "t@e.com")
	git(work, "config", "user.name", "t")

	bundle := "name: demo\nversion: \"1.0\"\n" +
		"mcp:\n  demo-server:\n    command: demo-mcp\n    args: [\"--stdio\"]\n" +
		"hooks:\n  session_start:\n    - command: echo demo-hook\n      type: command\n"
	bundlePath := filepath.Join(work, "ctxloom", "bundles", "demo.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(bundlePath), 0o755))
	require.NoError(t, os.WriteFile(bundlePath, []byte(bundle), 0o644))

	git(work, "add", "-A")
	git(work, "commit", "-m", "seed")
	git(work, "remote", "add", "origin", bare)
	git(work, "push", "origin", "main")
	git(bare, "symbolic-ref", "HEAD", "refs/heads/main")

	return "file://" + bare
}

// TestMCP_WireProtocol_SyncRoutesNewBundleThroughReview pins dreary-endurable-overturn:
// a runtime sync_dependencies that pulls a new (untrusted) bundle must NOT
// auto-apply that bundle's hooks. Instead it routes the change through the review
// gate (returning the review template) so the user sees the bundle before its
// arbitrary-code hooks run. After acknowledge_bundle_review the hooks apply.
//
// The bundle is referenced by a NON-default profile, so startup sync leaves it
// alone and the explicit sync_dependencies call is what surfaces it.
func TestMCP_WireProtocol_SyncRoutesNewBundleThroughReview(t *testing.T) {
	binPath := buildCtxloomBinary(t)

	remoteURL := seedFileRemote(t)

	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.ProfilesPath(appDir), 0o755))
	require.NoError(t, os.MkdirAll(paths.BundlesPath(appDir), 0o755))

	// Untrusted remote (not trusted) → a new bundle must go through review.
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"),
		[]byte("remotes:\n  demo-remote:\n    url: "+remoteURL+"\n    version: v1\n"), 0o644))
	// Non-default profile referencing the remote bundle.
	require.NoError(t, os.WriteFile(filepath.Join(paths.ProfilesPath(appDir), "extra.yaml"),
		[]byte("name: extra\nbundles:\n  - demo-remote/demo\n"), 0o644))
	// Minimal config with NO default profiles, so startup doesn't pre-sync it.
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"),
		[]byte("defaults:\n  profiles: []\n"), 0o644))

	claudeSettings := filepath.Join(workDir, ".claude", "settings.json")

	c := startMCPServer(t, binPath, workDir)
	initSession(t, c)

	// Drive the sync. It pulls demo from the file:// remote into the pending
	// lockfile and must route the change through review.
	resp := callTool(t, c, 10, "sync_dependencies", map[string]any{})
	body := firstContentText(t, resp)

	assert.Contains(t, body, "bundle review required",
		"sync of a new untrusted bundle must surface the review template")
	assert.Contains(t, body, "demo", "the new bundle should be named in the review")

	// Hooks must NOT have been auto-applied: the bundle's session_start hook
	// must be absent from .claude/settings.json while review is pending.
	require.False(t, fileContains(t, claudeSettings, "demo-hook"),
		"bundle hook must not be applied before the review is acknowledged")

	// Acknowledge → pending merges into active and hooks apply.
	ack := callTool(t, c, 11, "acknowledge_bundle_review", map[string]any{})
	require.False(t, resultIsError(t, ack), "acknowledge_bundle_review should succeed")

	assert.True(t, fileContains(t, claudeSettings, "demo-hook"),
		"bundle hook must be applied once the review is acknowledged")
}

func fileContains(t *testing.T, path, substr string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	return strings.Contains(string(data), substr)
}
