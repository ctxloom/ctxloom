package spawn

import (
	"context"
	"errors"
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
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// Owner ruling 2026-10-02: the session's own MCP server and hooks share its
// --disable-sig-check; an agent it DELEGATES to does not. The coordinator that
// resolves a delegated child lives in that MCP server, whose App is waived —
// so this is the place the waiver could leak, through the generation rather
// than through any environment variable.
func TestSpawner_ADelegatedChildOfAWaivedSessionDecidesEnforcedAndCarriesNoWaiver(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "fixture-not-a-token") // the auth check needs one exported; nothing runs
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: plan\n")
	src, err := configload.New(nil, nil, configload.WithAppDir(appDir))
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src, config.WithoutSignatureCheck())
	require.NoError(t, err)
	app := operations.OpenedApp(owner, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})
	s := newSpawner(termRep(), app, filepath.Dir(appDir), nil)

	// Control: the session's OWN engine is launched from the waived
	// generation, and its environment hands the waiver to the MCP server and
	// hooks the engine starts.
	hostDeps, err := app.LaunchDeps(context.Background())
	require.NoError(t, err)
	require.True(t, hostDeps.Snapshot.Trust.SignatureCheckDisabled(), "the session itself is waived")

	plan, err := s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.False(t, plan.Snapshot.Trust.SignatureCheckDisabled(), "the child's generation is enforced")
	assert.True(t, owner.Current().Trust.SignatureCheckDisabled(), "and the session stays waived")

	harp, err := s.AssignSession(filepath.Dir(appDir), plan.Backend)
	require.NoError(t, err)
	resolved, err := s.ResolveLaunch(context.Background(), plan, coord.SpawnStart{
		Identity: sessions.Identity{Harp: harp, Depth: 1, Project: "proj"}, Prompt: "hi",
	})
	require.NoError(t, err)
	assert.NotContains(t, resolved.Launch.EngineEnv(), bundles.SessionSigCheckEnv, "the delegated child's engine carries no waiver")
	assert.NotContains(t, resolved.Launch.EngineEnv(), bundles.SigCheckEnv)
}

// failingAfterOpen reads once (Open), then fails every later read: a spawn's
// reload that cannot happen.
type failingAfterOpen struct {
	config.Sources
	reads int
}

func (f *failingAfterOpen) Read(ctx context.Context) (*config.Config, []config.Warning, error) {
	f.reads++
	if f.reads > 1 {
		return nil, nil, errors.New("config went away")
	}
	return f.Sources.Read(ctx)
}

// A spawn whose reload fails falls back to the published generation — but
// never to a WAIVED one: that would hand the child the session's waiver.
func TestSpawner_AFailedReloadNeverFallsBackToAWaivedGeneration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    []config.Option
		refused bool
	}{
		{"enforced session falls back", nil, false},
		{"waived session refuses", []config.Option{config.WithoutSignatureCheck()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetStrictness(t)
			t.Setenv("HOME", t.TempDir())
			appDir := filepath.Join(t.TempDir(), ".ctxloom")
			writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n")
			inner, err := configload.New(nil, nil, configload.WithAppDir(appDir))
			require.NoError(t, err)
			owner, err := config.Open(context.Background(), &failingAfterOpen{Sources: inner}, tc.opts...)
			require.NoError(t, err)
			s := newSpawner(termRep(), operations.OpenedApp(owner, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims}), filepath.Dir(appDir), nil)

			_, err = s.Resolve(context.Background(), "dev")

			if tc.refused {
				require.Error(t, err)
				assert.Contains(t, err.Error(), errDelegationWaived)
				return
			}
			require.NoError(t, err, "an enforced published generation is a safe fallback")
		})
	}
}
