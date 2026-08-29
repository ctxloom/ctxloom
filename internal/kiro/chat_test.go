//go:build parked_engines

package kiro

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestChatACPConfig_UsesGenericModelFlag pins a verified fact: unlike
// codex-acp, `kiro-cli acp --model <id>` is a real, CLI-parse-accepted flag
// (verified live: `kiro-cli --help-all` documents it, and an unauthenticated
// live spawn with an arbitrary --model value fails on kiro-cli's own auth
// check rather than on argument parsing) — so kiro rides the driver's
// generic --model argv untouched, exactly like buildArgs already does for
// the oneshot `kiro-cli chat` path (backend.go). No ModelConfigKey/
// ModelEnvVar override is wired.
func TestChatACPConfig_UsesGenericModelFlag(t *testing.T) {
	b := NewKiro()
	cfg := b.chatACPConfig()
	assert.Equal(t, "kiro-cli acp", cfg.Command)
	assert.Equal(t, defaultAgentName, cfg.Agent)
	assert.Empty(t, cfg.ModelConfigKey, "kiro's --model flag works; no override needed")
	assert.Empty(t, cfg.ModelEnvVar, "kiro has no known model env var precedent; --model argv is the mechanism")
}

// TestChatACPConfig_CarriesConfiguredKnobs pins that agent/agent-engine/args/
// env configured onto the backend reach the embedded ACP driver unchanged.
func TestChatACPConfig_CarriesConfiguredKnobs(t *testing.T) {
	b := NewKiro()
	b.Configure(&KiroConfig{Agent: "custom-agent", AgentEngine: "v3", Env: map[string]string{"FOO": "bar"}})
	cfg := b.chatACPConfig()
	assert.Equal(t, "custom-agent", cfg.Agent)
	assert.Equal(t, "v3", cfg.AgentEngine)
	assert.Equal(t, map[string]string{"FOO": "bar"}, cfg.Env)
}

// A configured `effort:` is delivered on the direct-CLI path (buildArgs
// emits --effort) but the structured-chat path never reads it — the
// user's setting silently evaporates, and the session runs at kiro's default
// effort while nothing says so. Delivering it to `kiro-cli acp` is unverified
// (see chatACPConfig's doc), so the honest outcome is a loud one, not a quiet
// drop.
func TestChatACPConfig_DroppedEffortIsAnnounced(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	b := NewKiro()
	b.Configure(&KiroConfig{Effort: "xhigh"})
	_ = b.chatACPConfig()
	assert.Contains(t, buf.String(), "effort", "a dropped effort setting must be announced, not swallowed")
	assert.Contains(t, buf.String(), "xhigh", "the announcement must name the value that was not delivered")
}

// A backend with no effort configured stays quiet.
func TestChatACPConfig_NoEffortNoNoise(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	b := NewKiro()
	_ = b.chatACPConfig()
	assert.Empty(t, buf.String())
}
