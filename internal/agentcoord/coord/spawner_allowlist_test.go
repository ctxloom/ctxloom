package coord

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestCheckStartRunAllowlist pins the delegation allowlist: backends reviewed
// onto the StartRun path pass; anything else — a future backend never
// reviewed, a config-declared llm type that matches no backend, the test
// backend — is refused with an error that NAMES the replacement, not just a
// generic failure.
func TestCheckStartRunAllowlist(t *testing.T) {
	t.Run("StartRun backends pass", func(t *testing.T) {
		// mock is on the list because the binary hosts it (ctxloom llm host
		// mock); see TestProdSpawner_MockIsAdmittedBecauseTheBinaryHostsIt.
		for _, backend := range []string{"claude-code", "mock"} {
			assert.NoError(t, checkStartRunAllowlist(backend), "backend %q", backend)
		}
	})

	t.Run("anything else is refused, naming the path that exists", func(t *testing.T) {
		for _, backend := range []string{"futurebackend", ""} {
			err := checkStartRunAllowlist(backend)
			require.Error(t, err, "backend %q must not run delegated children", backend)
			assert.Contains(t, err.Error(), "StartRun")
			assert.Contains(t, err.Error(), "claude-code")
		}
	})
}

// TestProdSpawner_Resolve_Allowlist is the end-to-end refusal at the real
// Spawner.Resolve entry point: a config-declared llm entry whose type names an
// unreviewed backend fails loud at Resolve instead of resolving into a run
// nothing can drive.
func TestProdSpawner_Resolve_Allowlist(t *testing.T) {
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
		require.Error(t, err, "an llm type outside viaStartRunBackends must refuse")
		assert.Contains(t, err.Error(), "futurebackend")
		assert.Contains(t, err.Error(), "StartRun")
	})

	t.Run("a reviewed backend resolves", func(t *testing.T) {
		s := newSpawner(t, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n")
		plan, err := s.Resolve(context.Background(), "dev")
		require.NoError(t, err)
		assert.Equal(t, "claude-code", plan.Backend)
	})
}

// TestProdSpawner_MockIsAdmittedBecauseTheBinaryHostsIt pins the reason mock
// may run delegated children: it is on the StartRun allowlist, and it is there
// because `ctxloom llm host mock` stands up a real runner around it. The
// Starter seam is orthogonal — it swaps the runner for an in-process double
// and admits nothing on its own — and the allowlist admits nothing unreviewed.
func TestProdSpawner_MockIsAdmittedBecauseTheBinaryHostsIt(t *testing.T) {
	newSpawner := func(t *testing.T, starter StarterFunc) *prodSpawner {
		t.Helper()
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		appDir := filepath.Join(t.TempDir(), ".ctxloom")
		writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: mock\n    permissions: bypass\n")
		cfg, err := config.Load(config.WithAppDir(appDir))
		require.NoError(t, err)
		return newProdSpawner(cfg, filepath.Dir(appDir), starter)
	}

	t.Run("no Starter: mock resolves off the allowlist alone", func(t *testing.T) {
		plan, err := newSpawner(t, nil).Resolve(context.Background(), "dev")
		require.NoError(t, err, "mock is a hostable runner and needs no seam to be admitted")
		assert.Equal(t, "mock", plan.Backend)
	})

	t.Run("a Starter widens nothing: an unreviewed backend is still refused", func(t *testing.T) {
		starter := func(string, map[string]string) isolation.EngineStarter { return nil }
		require.Error(t, newSpawner(t, starter).admit("futurebackend"),
			"the seam changes how a runner is stood up, never whether a backend is admitted")
	})

	assert.True(t, viaStartRunBackends["mock"], "mock is reviewed onto StartRun because the binary hosts it")
}
