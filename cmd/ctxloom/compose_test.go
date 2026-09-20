package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The composition root builds exactly one config Owner and exactly one
// Coordinator per process, and both report through the ONE Reporter it chose
// — a second request for either is refused, not silently honoured. The two
// triggers are deterministic: a stray agents/*.yaml beside config.yaml makes
// the Owner's config report a migration finding on its first agent lookup,
// and a malformed launch tunable in the environment makes coord.New warn as
// it resolves its retry budget.
func TestCompose_OneOwnerOneCoordinatorOneReporter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var found report.Collector
	comp := compose(&found)
	assert.Equal(t, report.Sink(&found), comp.Reporter, "the Reporter the root hands the services is the one it built")

	appDir := t.TempDir()
	require.NoError(t, os.MkdirAll(paths.AgentsPath(appDir), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(paths.AgentsPath(appDir), "stray.yaml"), []byte("engine: mock\n"), 0o600))
	src, err := operations.ComposeSources(operations.Compose{Options: []configload.Option{configload.WithAppDir(appDir)}})
	require.NoError(t, err)

	owner, err := comp.OpenConfig(context.Background(), src)
	require.NoError(t, err)
	owner.Current().Config.LoadAgents()
	migrations := 0
	for _, f := range found.All() {
		if f.Kind == report.KindMigration {
			migrations++
		}
	}
	require.Equal(t, 1, migrations, "the Owner's config reports through the root's Reporter: %v", found.All())

	_, err = comp.OpenConfig(context.Background(), src)
	assert.ErrorIs(t, err, errSecondOwner, "one config Owner per process")

	t.Setenv(coord.EnvLaunchMaxAttempts, "nope")
	before := len(found.All())
	c, err := comp.NewCoordinator(coord.Options{App: operations.OpenedApp(owner), ProjectDir: t.TempDir(), StateDir: t.TempDir(), OwnerHarp: "owner-harp"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.Greater(t, len(found.All()), before, "the Coordinator reports through the root's Reporter")
	assert.Contains(t, found.All()[len(found.All())-1].Text, coord.EnvLaunchMaxAttempts)

	_, err = comp.NewCoordinator(coord.Options{App: operations.OpenedApp(owner), ProjectDir: t.TempDir(), StateDir: t.TempDir(), OwnerHarp: "owner-harp"})
	assert.ErrorIs(t, err, errSecondCoordinator, "one Coordinator per process")
}
