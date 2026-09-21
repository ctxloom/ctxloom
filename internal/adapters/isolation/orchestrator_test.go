package isolation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// THE MODEL (ruled 2026-09-21): the ORCHESTRATOR — the root session, the one
// with no parent — holds the WHOLE credential in its session home, two-way
// with the host file, and is the only ctxloom-side refresher. Every AGENT
// holds a read-only PROJECTION (no refresh token) of the ORCHESTRATOR's
// credential — never of the host file — re-projected whenever the
// orchestrator's changes; an agent write reaches nothing.

// orchestratorHarp is the root session every agent here projects from.
const orchestratorHarp = "ugly-icy-squid"

// seededOrchestrator lays the whole credential into the orchestrator's own
// session home (under the fake HOME) and returns its credential path.
func seededOrchestrator(t *testing.T, bytes string) string {
	t.Helper()
	home, err := paths.HarpSessionHome(orchestratorHarp)
	require.NoError(t, err)
	dir := filepath.Join(home, "claude")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, ".credentials.json")
	require.NoError(t, os.WriteFile(p, []byte(bytes), 0o600))
	return p
}

// (1) The orchestrator's seed is WHOLE and TWO-WAY: refresh token included;
// a host change reaches it; its change reaches the host.
func TestCopyAmbient_TheOrchestratorsSeedIsWholeAndTwoWay(t *testing.T) {
	hostFile := seededClaudeHome(t, []byte(hostOAuthCredential))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	instance := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = report.Close() })
	instFile := filepath.Join(instance, "claude", ".credentials.json")
	placed, err := os.ReadFile(instFile)
	require.NoError(t, err)
	assert.Equal(t, hostOAuthCredential, string(placed), "the orchestrator holds the whole credential, refresh token included")

	rotateByRename(t, hostFile, []byte(`{"claudeAiOauth":{"accessToken":"acc-2","refreshToken":"ref-2"}}`))
	eventuallyReads(t, instFile, `{"claudeAiOauth":{"accessToken":"acc-2","refreshToken":"ref-2"}}`)

	require.NoError(t, os.WriteFile(instFile, []byte(`{"claudeAiOauth":{"accessToken":"acc-3","refreshToken":"ref-3"}}`), 0o600))
	eventuallyReads(t, hostFile, `{"claudeAiOauth":{"accessToken":"acc-3","refreshToken":"ref-3"}}`)
}

// (2) An agent's seed is a projection OF THE ORCHESTRATOR's credential: the
// orchestrator's file changing re-projects every agent; the host file
// changing alone leaves agents untouched until the orchestrator adopts it.
func TestCopyAmbient_AnAgentsSeedIsAProjectionOfTheOrchestrators(t *testing.T) {
	hostFile := seededClaudeHome(t, []byte(`{"claudeAiOauth":{"accessToken":"host-acc","refreshToken":"host-ref"}}`))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	orch := seededOrchestrator(t, `{"claudeAiOauth":{"accessToken":"orch-acc","refreshToken":"orch-ref","expiresAt":1}}`)

	instance := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir(), Orchestrator: orchestratorHarp})
	require.NoError(t, err)
	t.Cleanup(func() { _ = report.Close() })
	_, oauth := seededOAuth(t, instance)
	assert.Equal(t, "orch-acc", oauth["accessToken"], "the agent projects the ORCHESTRATOR's credential, not the host's")
	assert.NotContains(t, oauth, "refreshToken")

	rotateByRename(t, hostFile, []byte(`{"claudeAiOauth":{"accessToken":"host-acc-2","refreshToken":"host-ref-2"}}`))
	time.Sleep(4 * replicationDebounce)
	_, oauth = seededOAuth(t, instance)
	assert.Equal(t, "orch-acc", oauth["accessToken"], "the host file alone changing does not reach an agent")

	rotateByRename(t, orch, []byte(`{"claudeAiOauth":{"accessToken":"orch-acc-2","refreshToken":"orch-ref-2","expiresAt":2}}`))
	require.Eventually(t, func() bool {
		_, o := seededOAuth(t, instance)
		return o["accessToken"] == "orch-acc-2"
	}, 5*time.Second, 25*time.Millisecond, "the orchestrator's rotation never reached the agent")
	_, oauth = seededOAuth(t, instance)
	assert.NotContains(t, oauth, "refreshToken", "re-projected on every change")
}

// (3) An agent's write reaches neither the orchestrator nor the host, and
// the agent is restored to the orchestrator's projection.
func TestCopyAmbient_AnAgentsWriteReachesNeitherTheOrchestratorNorTheHost(t *testing.T) {
	hostFile := seededClaudeHome(t, []byte(hostOAuthCredential))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	const orchBytes = `{"claudeAiOauth":{"accessToken":"orch-acc","refreshToken":"orch-ref"}}`
	orch := seededOrchestrator(t, orchBytes)

	instance := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir(), Orchestrator: orchestratorHarp})
	require.NoError(t, err)
	t.Cleanup(func() { _ = report.Close() })
	instFile := filepath.Join(instance, "claude", ".credentials.json")
	placed, _ := seededOAuth(t, instance)

	require.NoError(t, os.WriteFile(instFile, []byte(`{"claudeAiOauth":{"accessToken":"agent-wrote","refreshToken":"agent-ref"}}`), 0o600))
	time.Sleep(4 * replicationDebounce)
	got, err := os.ReadFile(orch)
	require.NoError(t, err)
	assert.Equal(t, orchBytes, string(got), "an agent write must not reach the orchestrator")
	got, err = os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, hostOAuthCredential, string(got), "an agent write must not reach the host")
	eventuallyReads(t, instFile, string(placed))
}

// An agent whose orchestrator has no credential to project is "nothing
// seedable" for that agent — never a fall back to the host file.
func TestCopyAmbient_AnAgentWithNoOrchestratorCredentialIsNoSource(t *testing.T) {
	seededClaudeHome(t, []byte(hostOAuthCredential))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	instance := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir(), Orchestrator: orchestratorHarp})
	require.NoError(t, err)
	t.Cleanup(func() { _ = report.Close() })
	assert.True(t, report.NoSource, "the host file is never an agent's source")
	assert.Contains(t, report.NoSourceReason, orchestratorHarp)
	assert.NoFileExists(t, filepath.Join(instance, "claude", ".credentials.json"))
}

// The instance's own .claude.json is unaffected by which credential it
// projects: read back to prove the file is a JSON object either way.
func TestCopyAmbient_AnAgentStillGetsItsInstanceConfig(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"hasCompletedOnboarding":true,"oauthAccount":{"emailAddress":"u@example.com"}}`), 0o600))
	seededOrchestrator(t, hostOAuthCredential)

	instance := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir(), Orchestrator: orchestratorHarp})
	require.NoError(t, err)
	t.Cleanup(func() { _ = report.Close() })
	data, err := os.ReadFile(filepath.Join(instance, "claude", ".claude.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	assert.Equal(t, map[string]any{"emailAddress": "u@example.com"}, cfg["oauthAccount"])
}
