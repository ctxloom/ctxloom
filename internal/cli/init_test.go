package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestGenerateConfig covers the ctxloom init config builder. The selected
// engine's backend type lands in the registry, and the output must be valid
// YAML ending in a newline.
func TestGenerateConfig(t *testing.T) {
	for _, engine := range []string{"claude-code", "antigravity", "codex"} {
		t.Run(engine, func(t *testing.T) {
			data, err := operations.BuildInitialConfig(engine)
			require.NoError(t, err)
			body := string(data)
			assert.Contains(t, body, "type: "+engine,
				"engine must appear as a registry entry type")
			assert.NotContains(t, body, "role:",
				"role is registry-only and stripped on write")
			assert.True(t, strings.HasSuffix(body, "\n"),
				"config must end with newline (POSIX-friendly + diff-friendly)")
		})
	}
}

func TestGenerateConfig_DefaultsBlock(t *testing.T) {
	data, err := operations.BuildInitialConfig("claude-code")
	require.NoError(t, err)
	body := string(data)
	// The scaffold settings survive into the written config.
	assert.Contains(t, body, "use_distilled: true")
	assert.Contains(t, body, "auto_register_ctxloom: true")
	// The engine's role pair is wired into llm.defaults.
	assert.Contains(t, body, "primary: claude-code")
	assert.Contains(t, body, "fast: claude-fast")
}

// TestPersonalRemoteRequests covers the pure request builder behind
// `ctxloom init`'s personal-repo registration: the first repo is named
// "personal" and the rest "personal-N", every request is trusted, and the
// --forge label (when set) binds every personal remote to that forge.
func TestPersonalRemoteRequests(t *testing.T) {
	t.Run("names, trust, and empty forge", func(t *testing.T) {
		reqs := personalRemoteRequests([]string{"me/a", "me/b", "me/c"}, "")
		require.Len(t, reqs, 3)
		assert.Equal(t, "personal", reqs[0].Name)
		assert.Equal(t, "personal-2", reqs[1].Name)
		assert.Equal(t, "personal-3", reqs[2].Name)
		for _, r := range reqs {
			assert.True(t, r.Trust, "personal repos are trusted by default")
			assert.Empty(t, r.Forge, "no --forge means resolution falls back to host-match")
		}
		assert.Equal(t, "me/a", reqs[0].URL)
	})

	t.Run("forge binds every personal remote", func(t *testing.T) {
		reqs := personalRemoteRequests([]string{"me/a", "me/b"}, "work-ghe")
		require.Len(t, reqs, 2)
		for _, r := range reqs {
			assert.Equal(t, "work-ghe", r.Forge, "--forge must bind each personal remote")
		}
	})

	t.Run("no repos yields no requests", func(t *testing.T) {
		assert.Empty(t, personalRemoteRequests(nil, "github"))
	})
}
