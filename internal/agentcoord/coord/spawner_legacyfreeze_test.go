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
		for _, backend := range []string{"claude-code", "codex", "kiro", "acp", "opencode"} {
			assert.NoError(t, checkLegacyChatFreeze(backend), "backend %q", backend)
		}
	})

	t.Run("frozen legacy residue passes (freeze, not removal)", func(t *testing.T) {
		for _, backend := range []string{"mock"} {
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
			// Both membership tables are named: the replacement path's
			// backends and the frozen residue still on the old one.
			assert.Contains(t, err.Error(), "claude-code")
			assert.Contains(t, err.Error(), "mock")
		}
	})
}

// TestLegacyChatBackendsOnlyEmpties pins the "emptying allowlist" property of
// the freeze: the frozen residue may only shrink. Every member must be in the
// CURRENT frozen set, and no member may simultaneously be on the StartRun
// path (a backend that migrates must LEAVE this table, not straddle both).
//
// The expected set is the residue as it stands NOW, not as it stood at
// retirement: the table was frozen with {opencode, mock} and the spool
// cutover's S3b slice performed the first (and only sanctioned) mutation —
// the SHRINK to {mock} — by migrating opencode onto viaStartRunBackends.
// Pinning the current set rather than the retirement set is what makes the
// property directional: re-adding opencode (or anything else) fails here,
// while a further shrink to the empty set does not.
func TestLegacyChatBackendsOnlyEmpties(t *testing.T) {
	stillFrozen := map[string]bool{"mock": true}
	for backend, ok := range legacyChatBackends {
		if !ok {
			continue
		}
		assert.True(t, stillFrozen[backend],
			"backend %q added to legacyChatBackends: the frozen residue admits no new members and never re-admits a migrated one — backends ride StartRun (viaStartRunBackends)", backend)
		assert.False(t, viaStartRunBackends[backend],
			"backend %q is in BOTH legacyChatBackends and viaStartRunBackends: a migrated backend must leave the frozen table", backend)
	}
	// The shrink itself, asserted directly: opencode is GONE from the frozen
	// residue and present on the replacement path. Without this, removing
	// opencode from viaStartRunBackends and restoring it to legacyChatBackends
	// (the exact revert this slice must not silently tolerate) would leave the
	// loop above entirely green.
	assert.False(t, legacyChatBackends["opencode"], "S3b removed opencode from the frozen residue; it must not return")
	assert.True(t, viaStartRunBackends["opencode"], "S3b migrated opencode onto the StartRun path")
}

// TestProdSpawner_Resolve_LegacyFreeze is the end-to-end refusal at the real
// Spawner.Resolve entry point: a config-declared llm entry whose type names
// an unreviewed backend used to resolve fine and silently ride the legacy
// driveChild loop (the per-turn oneshot fallback, since no such backend
// implements StructuredChat) — the house silent-no-op shape. It now fails
// loud at Resolve. The frozen resident (mock) still resolves, off the
// StartRun path, proving the gate freezes rather than removes — and
// opencode, migrated by S3b, resolves ONTO StartRun.
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

	t.Run("the frozen resident mock still resolves, off StartRun", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: mock\n    permissions: bypass\n")
		plan, err := s.Resolve(context.Background(), "dev")
		require.NoError(t, err, "a frozen resident is frozen ONTO the legacy path, not removed from delegation")
		assert.Equal(t, "mock", plan.Backend)
		assert.False(t, plan.ViaStartRun, "a frozen resident is not on the StartRun path; it rides the frozen legacy loop")
	})
}

// TestProdSpawner_Resolve_OpencodeViaStartRun is S3b's resolve-level proof at
// the REAL Spawner.Resolve entry point: an agent whose llm entry names
// opencode resolves onto the MIGRATED path, so its delegated children spawn a
// runner and take engine control over StartRun instead of the retired
// coordinator-driven go-plugin Chat dial (children.go's `rt.plan.ViaStartRun
// && url != ""` branch is keyed on exactly this field).
//
// Payload-asserted on the resolved launch shape rather than an error code:
// the backend that rode through unmodified, the ViaStartRun routing flag the
// spawn tail branches on, and the resume mode — opencode has no
// resume-by-key primitive (resumeCapableBackends), so migrating it must NOT
// have quietly made it one; a conversational opencode agent stays
// ResumeModePersistent.
func TestProdSpawner_Resolve_OpencodeViaStartRun(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: opencode\n    permissions: bypass\n")
	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	s := newProdSpawner(cfg, filepath.Dir(appDir), nil)

	plan, err := s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, "opencode", plan.Backend, "the resolved backend rides the plan unmodified")
	assert.True(t, plan.ViaStartRun,
		"opencode must resolve onto the StartRun path (S3b): with ViaStartRun false its children take the RETIRED legacy driveChild dial")
	assert.Equal(t, ResumeModePersistent, plan.ResumeMode,
		"migrating opencode's WIRE path must not claim a resume-by-key primitive it does not have")
	assert.NoError(t, checkLegacyChatFreeze(plan.Backend),
		"a migrated backend still passes the freeze gate — via viaStartRunBackends, not the residue")
}
