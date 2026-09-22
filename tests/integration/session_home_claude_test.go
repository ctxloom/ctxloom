//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// The claude arm of session-only delivery (ruled 2026-09-21): a default
// binding runs claude against the SESSION home — CLAUDE_CONFIG_DIR under
// ~/.ctxloom/sessions/<harp>/home — seeded whole from the host's credential
// (a bare run is the orchestrator; its agents get projections, pinned in
// the isolation package), the project and the real home untouched; a
// host with nothing seedable is refused by name; `engine_home: host` is the
// unsafe selection and is rendered as such. No live claude: a fake `claude`
// on PATH captures the launch (its env and argv) and answers the stream-json
// protocol with one reply.

// fakeClaudeScript answers `--version`, records its environment and argv,
// drains stdin, and speaks enough stream-json for one turn.
const fakeClaudeScript = `#!/bin/sh
case "$1" in --version) echo "2.1.278 (Claude Code)"; exit 0;; esac
env > "$FAKE_CLAUDE_CAPTURE.env"
printf '%s\n' "$@" > "$FAKE_CLAUDE_CAPTURE.argv"
cat > /dev/null
echo '{"type":"system","subtype":"init","session_id":"fake-native-session"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"FAKE-CLAUDE-REPLY"}]}}'
echo '{"type":"result","subtype":"success","num_turns":1}'
`

// hostCredentialWithRefresh is the host's ~/.claude/.credentials.json: the
// shape claude writes, refresh half included.
const hostCredentialWithRefresh = `{"claudeAiOauth":{"accessToken":"host-access","refreshToken":"host-refresh","refreshTokenExpiresAt":2,"expiresAt":1,"scopes":["user:inference"],"subscriptionType":"max"}}`

// setupClaudeSessionProject stands up a project with a profile, a fake
// claude on the child's PATH, and NO env token — so authentication can only
// come from a host credential file the test chooses to write.
func setupClaudeSessionProject(t *testing.T) (env *testenv.TestEnvironment, capturePath string) {
	t.Helper()
	env = setupTestEnv(t)
	writeFragment(t, env, "rules", []string{"rules"}, "Project rules for the session.")
	writeProfile(t, env, "dev", "name: dev\ndescription: dev\nbundles:\n  - local#fragments/rules\n")

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaudeScript), 0o755))
	capturePath = filepath.Join(t.TempDir(), "capture")
	env.SetChildEnv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env.SetChildEnv("FAKE_CLAUDE_CAPTURE", capturePath)
	env.SetChildEnv("CLAUDE_CODE_OAUTH_TOKEN", "")
	env.SetChildEnv("ANTHROPIC_API_KEY", "")
	// The suite itself may run inside a ctxloom session whose claude was
	// relocated; that CLAUDE_CONFIG_DIR is the developer's, not the child's.
	env.SetChildEnv("CLAUDE_CONFIG_DIR", "")
	return env, capturePath
}

// writeHostClaudeCredential lays the host credential (and a .claude.json)
// into the fake real home.
func writeHostClaudeCredential(t *testing.T, env *testenv.TestEnvironment) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(env.HomeDir, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(env.HomeDir, ".claude", ".credentials.json"), []byte(hostCredentialWithRefresh), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(env.HomeDir, ".claude.json"),
		[]byte(`{"hasCompletedOnboarding":true,"lastOnboardingVersion":"2.1.278","oauthAccount":{"emailAddress":"user@example.com"},"mcpServers":{"x":{"command":"secret"}}}`), 0o600))
}

// capturedEnv reads the fake claude's captured environment as a map.
func capturedEnv(t *testing.T, capturePath string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(capturePath + ".env")
	require.NoError(t, err, "the fake claude never ran")
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// TestRun_ClaudeDefaultBindingRunsInTheSessionHome: claude is told the
// session home as CLAUDE_CONFIG_DIR; that home carries the seeded credential
// WHOLE (this run is the root — the orchestrator, the single refresher) and
// a .claude.json with the account identity; the project tree and the real
// home are byte-identical before and after.
func TestRun_ClaudeDefaultBindingRunsInTheSessionHome(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	writeHostClaudeCredential(t, env)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	projectBefore := treeSnapshot(t, env.ProjectDir, projectExcluded...)
	homeBefore := treeSnapshot(t, env.HomeDir, paths.AppDirName)

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	require.Contains(t, env.LastOutput(), "FAKE-CLAUDE-REPLY")
	assert.NotContains(t, env.LastOutput(), "unsafe", "a default run names nothing unsafe")

	got := capturedEnv(t, capture)
	configDir := got["CLAUDE_CONFIG_DIR"]
	sessionsRoot := filepath.Join(env.HomeDir, paths.AppDirName, paths.SessionsDir)
	require.True(t, strings.HasPrefix(configDir, sessionsRoot+string(os.PathSeparator)),
		"CLAUDE_CONFIG_DIR %q must be the session home under %s", configDir, sessionsRoot)
	assert.Contains(t, configDir, string(os.PathSeparator)+paths.SessionEngineHomesDirName+string(os.PathSeparator))

	seeded, err := os.ReadFile(filepath.Join(configDir, ".credentials.json"))
	require.NoError(t, err, "the session home is seeded with the credential")
	var cred map[string]map[string]any
	require.NoError(t, json.Unmarshal(seeded, &cred))
	assert.Equal(t, "host-access", cred["claudeAiOauth"]["accessToken"])
	assert.Equal(t, "host-refresh", cred["claudeAiOauth"]["refreshToken"],
		"a bare `ctxloom run` is the ORCHESTRATOR: its session home holds the whole credential, two-way with the host — it is the one refresher; only its agents hold projections")
	info, err := os.Lstat(filepath.Join(configDir, ".credentials.json"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	cfgBytes, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
	require.NoError(t, err, ".claude.json is seeded beside the credential")
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(cfgBytes, &cfg))
	assert.Equal(t, map[string]any{"emailAddress": "user@example.com"}, cfg["oauthAccount"])
	assert.NotContains(t, cfg, "mcpServers")

	assert.Equal(t, projectBefore, treeSnapshot(t, env.ProjectDir, projectExcluded...), "a default claude run wrote the project tree")
	assert.Equal(t, homeBefore, treeSnapshot(t, env.HomeDir, paths.AppDirName), "a default claude run wrote the user's real home outside ~/.ctxloom")
	assert.NoFileExists(t, filepath.Join(env.ProjectDir, ".mcp.json"), "the MCP config lands in the session home, not the project")
	assert.NoFileExists(t, filepath.Join(env.ProjectDir, "CLAUDE.md"), "the context lands in the session home, not the project")
}

// TestRun_ClaudeWithNothingSeedableIsRefused: no host credential and no env
// token — the run is REFUSED, naming the three remedies, and nothing is
// written to the project; the fake claude never runs.
func TestRun_ClaudeWithNothingSeedableIsRefused(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	projectBefore := treeSnapshot(t, env.ProjectDir, projectExcluded...)

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.NotEqual(t, 0, env.LastExitCode(), "a host with nothing seedable must be refused:\n%s", env.LastOutput())
	out := env.LastOutput()
	for _, remedy := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "engine_home: host", "claude login"} {
		assert.Contains(t, out, remedy, "the refusal names the remedy %q", remedy)
	}
	assert.NoFileExists(t, capture+".env", "the engine must not be started on a home it cannot authenticate against")
	assert.Equal(t, projectBefore, treeSnapshot(t, env.ProjectDir, projectExcluded...), "a refused run wrote the project tree")
	assert.NoDirExists(t, filepath.Join(env.HomeDir, ".claude"), "a refused run never degrades to the real home")
}

// TestRun_ClaudeHostHomeSelectedIsUnsafeAndKeepsTheRealHome: the binding's
// explicit `engine_home: host` is rendered unsafe in the dry-run plan and
// the launch banner, and the run keeps the real home: no CLAUDE_CONFIG_DIR,
// no session-home credential — exactly the pre-ruling behaviour, now by
// selection.
func TestRun_ClaudeHostHomeSelectedIsUnsafeAndKeepsTheRealHome(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--engine-home", "host")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	_ = env.Run("run", "--agent", "dev", "--dry-run", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	assert.Contains(t, env.LastOutput(), `"engine_home": {
    "mode": "host",
    "unsafe": true
  }`, "the dry-run plan names the host selection unsafe")

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	require.Contains(t, env.LastOutput(), "FAKE-CLAUDE-REPLY")
	// The banner's one unsafe line names the host selection AND every kind
	// it forces to the project root: with no engine home to deliver
	// beneath, the plan routes claude's surfaces to the project tree, which
	// is exactly the sharing the selection is unsafe for.
	assert.Regexp(t, `(?m)^\s*unsafe: .*engine-home → host`, env.LastOutput(), "the launch banner names the host selection unsafe")
	assert.Contains(t, env.LastOutput(), "context → project-root", "a host-home run delivers its context to the project tree, named unsafe")

	got := capturedEnv(t, capture)
	assert.Empty(t, got["CLAUDE_CONFIG_DIR"], "a host-home run keeps the real home: claude is told no config dir")
	for _, d := range sessionDirs(t, env) {
		assert.Empty(t, findUnder(t, d, ".credentials.json"), "no credential is seeded into a session home the run does not use")
	}
}
