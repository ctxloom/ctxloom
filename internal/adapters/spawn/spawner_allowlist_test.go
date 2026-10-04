package spawn

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// TestCheckStartRunAllowlist pins the delegation allowlist: backends reviewed
// onto the StartRun path pass; anything else — a future backend never
// reviewed, a config-declared llm type that matches no backend, the test
// backend — is refused with an error that NAMES the replacement, not just a
// generic failure.
func TestCheckStartRunAllowlist(t *testing.T) {
	t.Run("StartRun backends pass", func(t *testing.T) {
		// mock is on the list because the binary hosts it (ctxloom runner
		// mock); see TestProdSpawner_MockIsAdmittedBecauseTheBinaryHostsIt.
		for _, backend := range []string{"claude-code", "mock"} {
			assert.NoError(t, checkStartRunAllowlist(engines.Registry(), backend), "backend %q", backend)
		}
	})

	t.Run("anything else is refused, naming the path that exists", func(t *testing.T) {
		for _, backend := range []string{"futurebackend", "", "mock-lossy"} {
			err := checkStartRunAllowlist(engines.Registry(), backend)
			require.Error(t, err, "backend %q must not run delegated children", backend)
			assert.Contains(t, err.Error(), "StartRun")
			assert.Contains(t, err.Error(), "claude-code")
		}
	})
}

// TestDelegationGates_ReadTheEngineDeclaration pins that both spawn gates
// are DERIVED from engine.Definition.DelegatedChildren: an engine nobody in
// this package has heard of is admitted the moment it declares delegated
// children, and refused, with its declared reason, when it declares them
// absent. Adding or removing an engine needs no edit here.
func TestDelegationGates_ReadTheEngineDeclaration(t *testing.T) {
	reg := enginefixture.RegistryOf(
		enginefixture.Kind("reviewed", mock.WithDelegation()),
		enginefixture.Kind("unreviewed"),
	)
	require.NoError(t, checkStartRunAllowlist(reg, "reviewed"))
	err := checkStartRunAllowlist(reg, "unreviewed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithDelegation", "the refusal carries the engine's declared reason")
	assert.Contains(t, err.Error(), "reviewed", "the refusal names the engines that ARE admitted")

	_, err = resolveResumeMode(reg, agents.DrivingOneshot, "reviewed")
	require.Error(t, err, "admitted but not ResumesByKey: oneshot must fail loud")
	mode, err := resolveResumeMode(engines.Registry(), agents.DrivingOneshot, "claude-code")
	require.NoError(t, err)
	assert.Equal(t, coord.ResumeModeOneShot, mode)
}

// TestProdSpawner_Resolve_Allowlist is the end-to-end refusal at the real
// Spawner.Resolve entry point: a config-declared llm entry whose type names an
// unreviewed backend fails loud at Resolve instead of resolving into a run
// nothing can drive.
func TestProdSpawner_Resolve_Allowlist(t *testing.T) {
	newSpawner := func(t *testing.T, body string) *spawner {
		t.Helper()
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		appDir := filepath.Join(t.TempDir(), ".ctxloom")
		writeSpawnerConfig(t, appDir, body)
		return newSpawner(termRep(), spawnerApp(t, appDir), filepath.Dir(appDir), nil)
	}

	t.Run("an unreviewed backend type is refused at Resolve", func(t *testing.T) {
		s := newSpawner(t, "schema_version: 6\nllm:\n  configs:\n    weird:\n      type: futurebackend\nagents:\n  dev:\n    llm: weird\n    permissions:\n      weird:\n        mode: bypass\n")
		_, err := s.Resolve(context.Background(), "dev")
		require.Error(t, err, "an llm type matching no delegating engine must refuse")
		assert.Contains(t, err.Error(), "futurebackend")
		assert.Contains(t, err.Error(), "StartRun")
	})

	t.Run("a reviewed backend resolves", func(t *testing.T) {
		s := newSpawner(t, "schema_version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: bypass\n")
		plan, err := s.Resolve(context.Background(), "dev")
		require.NoError(t, err)
		assert.Equal(t, "claude-code", plan.Backend)
	})
}

// TestProdSpawner_MockIsAdmittedBecauseTheBinaryHostsIt pins the reason mock
// may run delegated children: it is on the StartRun allowlist, and it is there
// because `ctxloom runner mock` stands up a real runner around it. The
// Starter seam is orthogonal — it swaps the runner for an in-process double
// and admits nothing on its own — and the allowlist admits nothing unreviewed.
func TestProdSpawner_MockIsAdmittedBecauseTheBinaryHostsIt(t *testing.T) {
	newSpawner := func(t *testing.T, starter StarterFunc) *spawner {
		t.Helper()
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		appDir := filepath.Join(t.TempDir(), ".ctxloom")
		writeSpawnerConfig(t, appDir, "schema_version: 6\nagents:\n  dev:\n    llm: mock\n    permissions:\n      mock:\n        mode: bypass\n")
		return newSpawner(termRep(), spawnerApp(t, appDir), filepath.Dir(appDir), starter)
	}

	t.Run("no Starter: mock resolves off the allowlist alone", func(t *testing.T) {
		plan, err := newSpawner(t, nil).Resolve(context.Background(), "dev")
		require.NoError(t, err, "mock is a hostable runner and needs no seam to be admitted")
		assert.Equal(t, "mock", plan.Backend)
	})

	t.Run("a Starter widens nothing: an unreviewed backend is still refused", func(t *testing.T) {
		starter := func(string, map[string]string) func(context.Context) (*isolation.RunnerHandle, error) { return nil }
		require.Error(t, newSpawner(t, starter).admit("futurebackend"),
			"the seam changes how a runner is stood up, never whether a backend is admitted")
	})

	_, admitted, _ := delegatedChildren(engines.Registry(), "mock")
	assert.True(t, admitted, "mock is reviewed onto StartRun because the binary hosts it")
}
