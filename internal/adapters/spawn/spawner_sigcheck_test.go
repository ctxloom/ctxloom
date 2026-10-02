package spawn

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// Owner ruling 2026-10-02 (corrected the same day): an agent DELEGATED from a
// session that waived the signature check is waived too — the same posture
// as its parent: unsigned and untrusted content admitted under the waiver's
// own reason, its own hooks handed the waiver, and the waiver recorded on its
// session — while everything that outranks the signature step (rejection,
// retraction, an unreadable approvals store) still refuses in the child.

// sigCheckSpawner is a spawner over a project declaring agent "dev", whose
// owner is opened with opts; it returns the spawner and the project's app dir.
func sigCheckSpawner(t *testing.T, opts ...config.Option) (*spawner, string) {
	t.Helper()
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "fixture-not-a-token") // the auth check needs one exported; nothing runs
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: plan\n")
	src, err := configload.New(nil, nil, configload.WithAppDir(appDir))
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src, opts...)
	require.NoError(t, err)
	app := operations.OpenedApp(owner, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})
	return newSpawner(termRep(), app, filepath.Dir(appDir), nil), appDir
}

// delegate resolves one child of agent "dev" through the spawner's real path.
func delegate(t *testing.T, s *spawner, projectDir string) (*coord.SpawnPlan, coord.Resolved, string) {
	t.Helper()
	plan, err := s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	harp, err := s.AssignSession(projectDir, plan.Backend)
	require.NoError(t, err)
	resolved, err := s.ResolveLaunch(context.Background(), plan, coord.SpawnStart{
		Identity: sessions.Identity{Harp: harp, Depth: 1, Project: "proj"}, Prompt: "hi",
	})
	require.NoError(t, err)
	return plan, resolved, harp
}

// unsignedRemoteExecutable is remote content nobody signed: what the waiver
// exists to admit.
func unsignedRemoteExecutable(t *testing.T) bundles.Exposure {
	t.Helper()
	br, err := trust.ParseBundleRef("ctxloom+git://github.com/acme/repo//bundles/tools#prompts/deploy")
	require.NoError(t, err)
	return bundles.Exposure{
		Read: bundles.NewRead("tools", &bundles.Bundle{Name: "tools"}, bundles.ProvenanceRemote, bundles.TrustCtxRemote,
			bundles.SignatureFacts{Signature: bundles.SignatureNone, Signer: bundles.SignerNone}),
		BundleRef: br, Bytes: []byte("#!/bin/sh\necho deploy\n"), Form: bundles.FormRaw,
	}
}

func TestSpawner_ADelegatedChildOfAWaivedSessionIsWaivedAndSaysSo(t *testing.T) {
	s, appDir := sigCheckSpawner(t, config.WithoutSignatureCheck())

	plan, resolved, harp := delegate(t, s, filepath.Dir(appDir))

	require.True(t, plan.Snapshot.Trust.SignatureCheckDisabled(), "the child decides with its parent's posture")
	v := plan.Snapshot.Trust.Authorizer().Admit(unsignedRemoteExecutable(t))
	assert.True(t, v.Allow, "unsigned remote content reaches the child")
	assert.Equal(t, bundles.ReasonSigCheckDisabled, v.Reason, "and its decision names the waiver")
	assert.Equal(t, sessions.SigCheckWaivedOn, resolved.Launch.EngineEnv()[sessions.EnvSigCheckWaived],
		"the child's engine hands its own hooks the waiver")
	entry, err := operations.GetSession(harp)
	require.NoError(t, err)
	assert.True(t, entry.SigCheckDisabled, "the child's session records that it ran waived")
	assert.Equal(t, sessions.OriginAgent, entry.Origin, "and that it is a delegated agent's")
}

func TestSpawner_ADelegatedChildOfAnEnforcedSessionIsEnforced(t *testing.T) {
	s, appDir := sigCheckSpawner(t)

	plan, resolved, harp := delegate(t, s, filepath.Dir(appDir))

	assert.False(t, plan.Snapshot.Trust.SignatureCheckDisabled())
	assert.False(t, plan.Snapshot.Trust.Authorizer().Admit(unsignedRemoteExecutable(t)).Allow)
	assert.NotContains(t, resolved.Launch.EngineEnv(), sessions.EnvSigCheckWaived)
	entry, err := operations.GetSession(harp)
	require.NoError(t, err)
	assert.False(t, entry.SigCheckDisabled)
	assert.Equal(t, sessions.OriginAgent, entry.Origin)
}

// The waiver a child inherits is the signature step's and nothing more: an
// approvals store that cannot be read still refuses in the child. (A
// rejection and a retraction outrank the signature step in the same cascade —
// composite's TestWithoutSignatureCheck_* — and the child's Trust is built by
// the same option.)
func TestSpawner_AWaivedChildStillRefusesWhenTheApprovalsStoreIsUnreadable(t *testing.T) {
	s, appDir := sigCheckSpawner(t, config.WithoutSignatureCheck())
	require.NoError(t, os.RemoveAll(paths.ApprovalsPath(appDir)))

	plan, err := s.Resolve(context.Background(), "dev")
	require.NoError(t, err)

	require.True(t, plan.Snapshot.Trust.SignatureCheckDisabled())
	v := plan.Snapshot.Trust.Authorizer().Admit(unsignedRemoteExecutable(t))
	assert.False(t, v.Allow, "a store fault is not the signature step, so the waiver does not reach it")
	assert.Equal(t, bundles.ReasonRecordsUnreadable, v.Reason)
}
