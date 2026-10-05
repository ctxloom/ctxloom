//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// The claude arm of session-only delivery: a default binding runs claude
// against the SESSION home — CLAUDE_CONFIG_DIR under
// ~/.ctxloom/sessions/<harp>/home — sharing the human's own login in place
// through CLAUDE_SECURESTORAGE_CONFIG_DIR with the setup-token blanked, so it
// runs with or without an exported token; no credential is copied into the
// home, and the project and the real home are untouched; `engine_home: host`
// is the unsafe selection and is rendered as such. No live claude: a fake `claude`
// on PATH captures the launch (its env and argv) and answers the stream-json
// protocol with one reply; it reports claude's version floor, which the
// runner checks before launch. Each agent declares `permissions: {mode: plan}`.

// fakeClaudeScript answers `--version` with claude's declared floor (in
// claude's own "<version> (Claude Code)" shape), records its environment and
// argv, snapshots the config dir it was handed (its listing, and its
// .claude.json) and, when a test names them, a records dir and one file,
// drains stdin, and speaks enough stream-json for one turn.
// The snapshot is taken WHILE THE RUN IS LIVE because the session home is
// disposable: Close removes it, so a post-run look would find nothing and
// every "nothing was copied in" assertion would pass vacuously. A delivery
// is the same: the run's teardown releases it, and its record goes with it.
func fakeClaudeScript(t *testing.T) string {
	t.Helper()
	e, ok := engines.Registry().Lookup(claude.EngineName)
	require.True(t, ok, "claude is composed")
	return fmt.Sprintf(fakeClaudeScriptBody, e.Root().Version.Floor)
}

const fakeClaudeScriptBody = `#!/bin/sh
case "$1" in --version) echo "%s (Claude Code)"; exit 0;; esac
env > "$FAKE_CLAUDE_CAPTURE.env"
printf '%%s\n' "$@" > "$FAKE_CLAUDE_CAPTURE.argv"
if [ -n "$CLAUDE_CONFIG_DIR" ]; then
  ls -A "$CLAUDE_CONFIG_DIR" > "$FAKE_CLAUDE_CAPTURE.home"
  if [ -f "$CLAUDE_CONFIG_DIR/.claude.json" ]; then cp "$CLAUDE_CONFIG_DIR/.claude.json" "$FAKE_CLAUDE_CAPTURE.claude.json"; fi
fi
if [ -n "$FAKE_CLAUDE_SNAPSHOT_RECORDS" ]; then cp -Rp "$FAKE_CLAUDE_SNAPSHOT_RECORDS" "$FAKE_CLAUDE_CAPTURE.records"; fi
if [ -n "$FAKE_CLAUDE_SNAPSHOT_FILE" ]; then cp -p "$FAKE_CLAUDE_SNAPSHOT_FILE" "$FAKE_CLAUDE_CAPTURE.file"; fi
cat > /dev/null
echo '{"type":"system","subtype":"init","session_id":"fake-native-session"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"FAKE-CLAUDE-REPLY"}]}}'
echo '{"type":"result","subtype":"success","num_turns":1}'
`

// hostCredentialWithRefresh is the host's own native ~/.claude login, which
// ctxloom must never copy.
const hostCredentialWithRefresh = `{"claudeAiOauth":{"accessToken":"host-access","refreshToken":"host-refresh","refreshTokenExpiresAt":2,"expiresAt":1,"scopes":["user:inference"],"subscriptionType":"max"}}`

// setupClaudeSessionProject stands up a project with a profile, a fake
// claude on the child's PATH, and NO env token — so authentication can only
// come from a token the test chooses to export.
// configureSessionLogin sets the top-level `auth: login`: the human's own
// session (a `ctxloom run`) shares their claude login. No agent ctxloom
// spawns ever does.
func configureSessionLogin(t *testing.T, env *testenv.TestEnvironment) {
	t.Helper()
	p := filepath.Join(env.ProjectDir, ".ctxloom", "config.yaml")
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	require.NoError(t, os.WriteFile(p, append(data, "auth: login\n"...), 0o644))
}

func setupClaudeSessionProject(t *testing.T) (env *testenv.TestEnvironment, capturePath string) {
	t.Helper()
	env = setupTestEnv(t)
	writeFragment(t, env, "rules", []string{"rules"}, "Project rules for the session.")
	writeProfile(t, env, "dev", "description: dev\nbundles:\n  - local#fragments/rules\n")

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaudeScript(t)), 0o755))
	capturePath = filepath.Join(t.TempDir(), "capture")
	env.SetChildEnv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env.SetChildEnv("FAKE_CLAUDE_CAPTURE", capturePath)
	env.SetChildEnv("CLAUDE_CODE_OAUTH_TOKEN", "")
	env.SetChildEnv("ANTHROPIC_API_KEY", "")
	// The suite itself may run inside a ctxloom session whose claude was
	// relocated; that CLAUDE_CONFIG_DIR and credential storage are the
	// developer's, not the child's. Both empty: the fake human's claude
	// resolves its login from $HOME/.claude.
	env.SetChildEnv("CLAUDE_CONFIG_DIR", "")
	env.SetChildEnv(secureStorageEnv, "")
	return env, capturePath
}

// writeHostClaudeCredential lays the host's native login (and a .claude.json)
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

// capturedHome is the listing of the config dir the fake claude was handed,
// taken while the run was live.
func capturedHome(t *testing.T, capturePath string) []string {
	t.Helper()
	data, err := os.ReadFile(capturePath + ".home")
	require.NoError(t, err, "the fake claude was handed no config dir")
	return strings.Fields(string(data))
}

// exportedSetupToken stands for what `claude setup-token` prints, which the
// human exports.
const exportedSetupToken = "sk-ant-oat01-integration-fixture"

// secureStorageEnv is the var that moves only claude's credential storage.
const secureStorageEnv = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

// requireSharesTheHumansLogin asserts the host run's shared login: the
// storage var is SET to "" (what the fake human's claude resolves: its
// $HOME/.claude), and the setup-token is blanked so it cannot shadow it.
func requireSharesTheHumansLogin(t *testing.T, got map[string]string) {
	t.Helper()
	storage, ok := got[secureStorageEnv]
	require.True(t, ok, "a host run is handed %s", secureStorageEnv)
	assert.Empty(t, storage, "the human's claude resolves its login from $HOME/.claude")
	assert.Empty(t, got["CLAUDE_CODE_OAUTH_TOKEN"], "the setup-token is blanked on the host")
}

// TestRun_ClaudeLoginAgentRunsInTheSessionHome: even with a token exported,
// the human's own host session under `auth: login` shares their login and is handed
// the token blank; claude is told the session home as CLAUDE_CONFIG_DIR; that
// home holds a .claude.json with the account identity and NO credential; the
// project tree and the real home are byte-identical before and after.
func TestRun_ClaudeLoginAgentRunsInTheSessionHome(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	writeHostClaudeCredential(t, env)
	env.SetChildEnv("CLAUDE_CODE_OAUTH_TOKEN", exportedSetupToken)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	configureSessionLogin(t, env)

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

	requireSharesTheHumansLogin(t, got)
	assert.NotContains(t, capturedHome(t, capture), ".credentials.json", "no credential is copied into the session home")
	assert.NotContains(t, env.LastOutput(), exportedSetupToken, "the run never prints the token")
	assert.NoDirExists(t, configDir, "the session home is disposable: Close removes it")

	cfgBytes, err := os.ReadFile(capture + ".claude.json")
	require.NoError(t, err, ".claude.json is generated in the session home")
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(cfgBytes, &cfg))
	assert.Equal(t, map[string]any{"emailAddress": "user@example.com"}, cfg["oauthAccount"])
	assert.NotContains(t, cfg, "mcpServers")

	assert.Equal(t, projectBefore, treeSnapshot(t, env.ProjectDir, projectExcluded...), "a default claude run wrote the project tree")
	assert.Equal(t, homeBefore, treeSnapshot(t, env.HomeDir, paths.AppDirName), "a default claude run wrote the user's real home outside ~/.ctxloom")
	assert.NoFileExists(t, filepath.Join(env.ProjectDir, ".mcp.json"), "the MCP config lands in the session home, not the project")
	assert.NoFileExists(t, filepath.Join(env.ProjectDir, "CLAUDE.md"), "the context lands in the session home, not the project")
}

// TestRun_ClaudeLoginAgentWithNoTokenSharesTheHumansLogin: no
// exported token and no API key — a host login agent still proceeds, on the
// human's own login shared in place, and nothing is copied into the session
// home or written to the project or the real home.
func TestRun_ClaudeLoginAgentWithNoTokenSharesTheHumansLogin(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	writeHostClaudeCredential(t, env)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	configureSessionLogin(t, env)
	projectBefore := treeSnapshot(t, env.ProjectDir, projectExcluded...)
	homeBefore := treeSnapshot(t, env.HomeDir, paths.AppDirName)

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), "a host run with no token proceeds on the human's login:\n%s", env.LastOutput())
	require.Contains(t, env.LastOutput(), "FAKE-CLAUDE-REPLY")

	got := capturedEnv(t, capture)
	requireSharesTheHumansLogin(t, got)
	assert.NotContains(t, capturedHome(t, capture), ".credentials.json", "the login is shared in place, never copied")
	assert.Equal(t, projectBefore, treeSnapshot(t, env.ProjectDir, projectExcluded...), "the run wrote the project tree")
	assert.Equal(t, homeBefore, treeSnapshot(t, env.HomeDir, paths.AppDirName), "the run wrote the user's real home outside ~/.ctxloom")
	for _, d := range sessionDirs(t, env) {
		assert.Empty(t, findUnder(t, d, ".credentials.json"), "a shared login copies nothing")
	}
}

// TestRun_ClaudeTokenAgentGetsTheExportedToken: an agent declaring no auth
// is a token agent. It is handed the token the human exported, and an API
// key the human exported is blanked: the declared mode decides.
func TestRun_ClaudeTokenAgentGetsTheExportedToken(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	env.SetChildEnv("ANTHROPIC_API_KEY", "sk-ant-api-shell")
	env.SetChildEnv("CLAUDE_CODE_OAUTH_TOKEN", exportedSetupToken)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	assert.NotContains(t, env.LastOutput(), exportedSetupToken, "the run never prints the token")
	got := capturedEnv(t, capture)
	assert.Equal(t, exportedSetupToken, got["CLAUDE_CODE_OAUTH_TOKEN"], "the exported token reaches the engine")
	assert.Empty(t, got["ANTHROPIC_API_KEY"], "the exported key is blanked for a token agent")
}

// TestRun_ClaudeTokenAgentWithNoTokenExportedIsRefused: a token agent with
// no token exported is refused before the engine starts, naming how the
// human mints and exports one; nothing prompts.
func TestRun_ClaudeTokenAgentWithNoTokenExportedIsRefused(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.NotEqual(t, 0, env.LastExitCode(), env.LastOutput())
	assert.Contains(t, env.LastOutput(), "claude setup-token")
	assert.NoFileExists(t, capture+".env", "the engine never started")
}

// TestRun_ClaudeHostHomeSelectedIsUnsafeAndKeepsTheRealHome: the
// binding's explicit `engine_home: host` is rendered unsafe in the dry-run
// plan and the launch banner, and the run keeps the real home: no
// CLAUDE_CONFIG_DIR, no session-home credential — exactly the pre-ruling
// behaviour, now by selection.
//
// With no engine home to deliver beneath, the MCP servers go to the
// project's own .mcp.json (the unsafe-file approach), which claude loads
// only for a repository the human trusted — so the project is trusted in
// the fake human's ~/.claude.json. That file is one teams commit: the
// relay bearer claude received is on disk nowhere ctxloom writes, the
// project's file and its §9.7 record naming it only by reference.
func TestRun_ClaudeHostHomeSelectedIsUnsafeAndKeepsTheRealHome(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	writeHostClaudeCredential(t, env)
	trustProjectOnHost(t, env)
	createHostHomeAgent(t, env)
	records := filepath.Join(env.HomeDir, paths.AppDirName, paths.HomeRecordsDirName)
	mcpFile := filepath.Join(env.ProjectDir, claude.MCPFileName)
	env.SetChildEnv("FAKE_CLAUDE_SNAPSHOT_RECORDS", records)
	env.SetChildEnv("FAKE_CLAUDE_SNAPSHOT_FILE", mcpFile)

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

	bearer := got[claude.EnvRelayBearer]
	require.NotEmpty(t, bearer, "claude's environment carries the relay bearer the project .mcp.json names")
	liveRecords, liveMCP := capture+".records", capture+".file"
	for _, dir := range []string{liveRecords, liveMCP, records, env.ProjectDir} {
		assert.Empty(t, filesHolding(t, dir, bearer), "the relay bearer is on disk under %s", dir)
	}
	assertRecordStatesTheRelayByReference(t, liveRecords, liveMCP, mcpFile)
}

// assertRecordStatesTheRelayByReference: the project .mcp.json claude was
// handed names the relay bearer by reference, and its record — read through
// the production decoder from the live snapshot — claims that very entry.
func assertRecordStatesTheRelayByReference(t *testing.T, liveRecords, liveMCP, target string) {
	t.Helper()
	data, err := os.ReadFile(liveMCP)
	require.NoError(t, err, "the project .mcp.json stood while claude ran")
	var doc struct {
		MCPServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	require.Contains(t, doc.MCPServers, claude.AppMCPServerName)
	assert.Equal(t, "${"+claude.EnvRelayBearer+"}", doc.MCPServers[claude.AppMCPServerName].Env[claude.EnvRelayBearer],
		"the project .mcp.json states the relay bearer by reference")

	targetFS := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(targetFS, target, data, 0o644))
	rec, err := fsstatic.NewRecords(afero.NewOsFs(), liveRecords)
	require.NoError(t, err)
	states, err := rec.Paths(targetFS, target)
	require.NoError(t, err)
	entry := "/mcpServers/" + claude.AppMCPServerName
	i := slices.IndexFunc(states, func(s delivery.PathState) bool { return s.Pointer == entry })
	require.GreaterOrEqual(t, i, 0, "the project .mcp.json's record claims %s", entry)
	assert.True(t, states[i].Live, "the record's claimed %s is the by-reference entry claude was handed", entry)
}

// TestRun_ClaudeHostHomeOnAnUntrustedRepositoryIsRefused: the same host-home
// binding on a repository the human never trusted is refused before claude
// starts, by the typed refusal: strict MCP mode ignores the project's
// .mcp.json, so the run would launch without ctxloom's servers.
func TestRun_ClaudeHostHomeOnAnUntrustedRepositoryIsRefused(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	writeHostClaudeCredential(t, env)
	createHostHomeAgent(t, env)

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.NotEqual(t, 0, env.LastExitCode(), env.LastOutput())
	assert.Contains(t, env.LastOutput(), claude.ErrUntrustedProjectMCP.Error())
	assert.NoFileExists(t, capture+".env", "the engine never started")
	assert.NoFileExists(t, filepath.Join(env.ProjectDir, claude.MCPFileName), "the refused run's delivery is reversed")
}

// createHostHomeAgent creates the agent the host-home cases run: auth
// login, the real home with the human's own login in place. Auth is per
// agent, so a host-home binding still declares how it authenticates.
func createHostHomeAgent(t *testing.T, env *testenv.TestEnvironment) {
	t.Helper()
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--engine-home", "host", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	configureSessionLogin(t, env)
}

// trustProjectOnHost records, in the fake human's ~/.claude.json, their
// acceptance of claude's trust prompt for the project — the record claude's
// repository verdict reads.
func trustProjectOnHost(t *testing.T, env *testenv.TestEnvironment) {
	t.Helper()
	path := filepath.Join(env.HomeDir, ".claude.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		require.NoError(t, json.Unmarshal(raw, &cfg))
	}
	cfg["projects"] = map[string]any{env.ProjectDir: map[string]any{"hasTrustDialogAccepted": true}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}

// filesHolding reports every regular file under dir whose bytes contain s.
func filesHolding(t *testing.T, dir, s string) []string {
	t.Helper()
	var hits []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), s) {
			hits = append(hits, p)
		}
		return nil
	})
	return hits
}

// TestRun_ClaudeLoginAgentWithNoLoginIsRefused: a login session whose human
// has no claude login on this host (no ~/.claude) is refused before the
// engine starts, naming the missing directory and `auth: token`, rather than
// started logged out.
func TestRun_ClaudeLoginAgentWithNoLoginIsRefused(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	configureSessionLogin(t, env)

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.NotEqual(t, 0, env.LastExitCode(), env.LastOutput())
	assert.Contains(t, env.LastOutput(), filepath.Join(env.HomeDir, ".claude"), "the remedy names the missing store")
	assert.Contains(t, env.LastOutput(), "auth: token")
	assert.NotContains(t, env.LastOutput(), "runtime is not available", "a missing login is not a runtime failure")
	assert.NoFileExists(t, capture+".env", "the engine never started")
}

// credentialSentinel stands in for an exported credential in the exposure
// audit: a string nothing else produces.
const credentialSentinel = "sk-ant-oat01-EXPOSURE-SENTINEL-7Q2"

// TestRun_TheCredentialIsNeverLoggedPersistedOrEchoed: a token agent's run
// reaches its engine with the exported credential and leaves no copy of it
// anywhere ctxloom writes — its output, its logs and session state under the
// ctxloom home, the project, the real home, or the temp dir its runner and
// launchers use. It lives only in the environment it was exported in.
func TestRun_TheCredentialIsNeverLoggedPersistedOrEchoed(t *testing.T) {
	env, capture := setupClaudeSessionProject(t)
	tmp := t.TempDir()
	env.SetChildEnv("TMPDIR", tmp)
	env.SetChildEnv("CLAUDE_CODE_OAUTH_TOKEN", credentialSentinel)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "claude-code", "--permissions", "plan")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	output := env.LastOutput()
	_ = env.Run("auth", "status", "--format", "json")
	output += env.LastOutput()

	require.Equal(t, credentialSentinel, capturedEnv(t, capture)["CLAUDE_CODE_OAUTH_TOKEN"], "fixture: the engine did receive it")
	assert.NotContains(t, output, credentialSentinel, "no command's output echoes the credential")
	for _, root := range []string{env.HomeDir, env.ProjectDir, tmp} {
		require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil {
				assert.NotContains(t, string(b), credentialSentinel, "%s holds the credential", p)
			}
			return nil
		}))
	}
}
