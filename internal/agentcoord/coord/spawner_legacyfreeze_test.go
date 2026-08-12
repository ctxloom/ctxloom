package coord

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestCheckLegacyChatFreeze pins the RETIRE-FIRST freeze gate for the legacy
// coordinator-driven chat path (children.go's driveChild): backends on the
// StartRun path and the FROZEN legacy residue pass; anything else — a future
// backend never reviewed onto StartRun, or a config-declared llm type that
// matches no backend — is refused with an error that NAMES the retirement and
// the replacement, not just a generic failure.
func TestCheckLegacyChatFreeze(t *testing.T) {
	t.Run("StartRun backends pass", func(t *testing.T) {
		for _, backend := range []string{"claude-code", "codex", "kiro", "acp"} {
			assert.NoError(t, checkLegacyChatFreeze(backend), "backend %q", backend)
		}
	})

	t.Run("frozen legacy residue passes (freeze, not removal)", func(t *testing.T) {
		for _, backend := range []string{"opencode", "mock"} {
			assert.NoError(t, checkLegacyChatFreeze(backend), "backend %q", backend)
		}
	})

	t.Run("anything else is refused, loudly naming the retirement", func(t *testing.T) {
		for _, backend := range []string{"futurebackend", ""} {
			err := checkLegacyChatFreeze(backend)
			require.Error(t, err, "backend %q must not be swept onto the retired path", backend)
			// Payload assertions: the refusal must say WHAT is retired and
			// WHAT replaces it, so an operator hitting it can act.
			assert.Contains(t, err.Error(), "legacy coordinator-driven chat path is retired")
			assert.Contains(t, err.Error(), "StartRun")
			assert.Contains(t, err.Error(), "claude-code")
			assert.Contains(t, err.Error(), "opencode")
		}
	})
}

// TestLegacyChatBackendsOnlyEmpties pins the "emptying allowlist" property of
// the freeze: the frozen residue may only shrink. Every member must be one of
// the two backends frozen onto the path at retirement time (opencode, mock),
// and no member may simultaneously be on the StartRun path (a backend that
// migrates must LEAVE this table, not straddle both).
func TestLegacyChatBackendsOnlyEmpties(t *testing.T) {
	frozenAtRetirement := map[string]bool{"opencode": true, "mock": true}
	for backend, ok := range legacyChatBackends {
		if !ok {
			continue
		}
		assert.True(t, frozenAtRetirement[backend],
			"backend %q added to legacyChatBackends: the frozen residue admits no new members — new backends must ride StartRun (viaStartRunBackends)", backend)
		assert.False(t, viaStartRunBackends[backend],
			"backend %q is in BOTH legacyChatBackends and viaStartRunBackends: a migrated backend must leave the frozen table", backend)
	}
}

// TestProdSpawner_Resolve_LegacyFreeze is the end-to-end refusal at the real
// Spawner.Resolve entry point: a config-declared llm entry whose type names
// an unreviewed backend used to resolve fine and silently ride the legacy
// driveChild loop (the per-turn oneshot fallback, since no such backend
// implements StructuredChat) — the house silent-no-op shape. It now fails
// loud at Resolve. The frozen resident (opencode) still resolves, off the
// StartRun path, proving the gate freezes rather than removes.
func TestProdSpawner_Resolve_LegacyFreeze(t *testing.T) {
	newSpawner := func(t *testing.T, body string) *prodSpawner {
		t.Helper()
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		appDir := filepath.Join(t.TempDir(), ".ctxloom")
		writeSpawnerConfig(t, appDir, body)
		cfg, err := config.Load(config.WithAppDir(appDir))
		require.NoError(t, err)
		return newProdSpawner(cfg, filepath.Dir(appDir), nil)
	}

	t.Run("an unreviewed backend type is refused at Resolve", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nllm:\n  configs:\n    weird:\n      type: futurebackend\nagents:\n  dev:\n    llm: weird\n    permissions: bypass\n")
		_, err := s.Resolve(context.Background(), "dev")
		require.Error(t, err, "an llm type outside viaStartRunBackends+legacyChatBackends must refuse, not silently join the retired path")
		assert.Contains(t, err.Error(), "futurebackend")
		assert.Contains(t, err.Error(), "legacy coordinator-driven chat path is retired")
		assert.Contains(t, err.Error(), "StartRun")
	})

	t.Run("the frozen resident opencode still resolves, off StartRun", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: opencode\n    permissions: bypass\n")
		plan, err := s.Resolve(context.Background(), "dev")
		require.NoError(t, err, "opencode is frozen ONTO the legacy path, not removed from delegation")
		assert.Equal(t, "opencode", plan.Backend)
		assert.False(t, plan.ViaStartRun, "opencode is not on the StartRun path; it rides the frozen legacy loop")
	})
}
