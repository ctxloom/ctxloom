package kiro

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestWriteInstanceConfig_ContributesNothingByDeclaration pins kiro's answer as
// a DECLARATION rather than an inference. An engine missing from the roster and
// an engine that genuinely contributes nothing look identical to a caller, and
// only one of them is a fact about the engine — the reason the method exists at
// all despite doing nothing.
//
// It writes NOTHING, into an instance home carrying realistic content, so a
// future implementation that started copying kiro's global agents/steering
// (exactly what a fresh KIRO_HOME exists to keep an agent run away from) goes
// red here.
func TestWriteInstanceConfig_ContributesNothingByDeclaration(t *testing.T) {
	host := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(host, ".kiro", "steering"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(host, ".kiro", "steering", "personal.md"), []byte("my own steering\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(host, ".kiro", "agents"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(host, ".kiro", "agents", "mine.json"), []byte(`{"name":"mine"}`), 0o644))

	instance := t.TempDir()
	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err, "an empty contribution is a normal outcome, never a fault")
	assert.Empty(t, rep.Wrote)
	assert.Empty(t, rep.Warnings)

	entries, err := os.ReadDir(instance)
	require.NoError(t, err)
	assert.Empty(t, entries, "kiro's instance is generated and seeded only — nothing ambient crosses")
}
