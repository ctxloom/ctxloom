package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// companionHome is a temp HOME whose .ctxloom is the App's pinned target (as
// `companion add` pins it), plus a PATH directory holding an answering
// ctxloom-companion-acme. PATH resolution is the real one.
func companionHome(t *testing.T) (*App, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script companions")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(t.TempDir(), "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ctxloom-companion-acme"),
		[]byte("#!/bin/sh\n[ \"$1\" = loadout ] && printf 'version: 1.0.0\\n' && exit 0\nexit 1\n"), 0o755)) //nolint:gosec // an executable fixture
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ctxloom-companion-mute"),
		[]byte("#!/bin/sh\nexit 2\n"), 0o755)) //nolint:gosec // an executable fixture
	t.Setenv("PATH", bin)
	t.Cleanup(companions.SetLookPathForTesting(exec.LookPath))
	appDir, err := paths.HomeConfigDir()
	require.NoError(t, err)
	return testApp(t, configload.WithAppDir(appDir)), appDir, bin
}

func readHomeConfig(t *testing.T, appDir string) string {
	t.Helper()
	b, err := os.ReadFile(paths.ConfigPath(appDir))
	require.NoError(t, err)
	return string(b)
}

func TestAddCompanion_RecordsOnlyTheName(t *testing.T) {
	app, appDir, bin := companionHome(t)

	res, err := AddCompanion(context.Background(), app, "acme")
	require.NoError(t, err)
	assert.Equal(t, CompanionAddResult{Name: "acme", Bin: "ctxloom-companion-acme", Path: filepath.Join(bin, "ctxloom-companion-acme"), Added: true}, res)

	written := readHomeConfig(t, appDir)
	assert.Contains(t, written, "companions:\n  - acme\n")
	assert.NotContains(t, written, bin, "the resolved path is reported, never recorded")
	assert.NotContains(t, written, "ctxloom-companion-acme", "the name is recorded, not the binary")
}

func TestAddCompanion_AlreadyRegisteredIsIdempotent(t *testing.T) {
	app, appDir, _ := companionHome(t)
	_, err := AddCompanion(context.Background(), app, "acme")
	require.NoError(t, err)
	res, err := AddCompanion(context.Background(), app, "acme")
	require.NoError(t, err)
	assert.False(t, res.Added)
	assert.Contains(t, readHomeConfig(t, appDir), "companions:\n  - acme\n")
}

func TestAddCompanion_RefusesWhatDoesNotAnswerAndRecordsNothing(t *testing.T) {
	app, appDir, _ := companionHome(t)
	_, err := AddCompanion(context.Background(), app, "mute")
	require.ErrorIs(t, err, companions.ErrNotACompanion)
	_, err = AddCompanion(context.Background(), app, "nowhere")
	require.ErrorIs(t, err, companions.ErrCompanionNotOnPath)
	_, statErr := os.Stat(paths.ConfigPath(appDir))
	assert.True(t, os.IsNotExist(statErr), "a refused add writes nothing")
}

func TestRemoveCompanion_StopsTheRegistration(t *testing.T) {
	app, appDir, _ := companionHome(t)
	_, err := AddCompanion(context.Background(), app, "acme")
	require.NoError(t, err)

	require.NoError(t, RemoveCompanion(context.Background(), app, "acme"))
	assert.NotContains(t, readHomeConfig(t, appDir), "acme")

	cfg, err := app.Config(context.Background())
	require.NoError(t, err)
	assert.Empty(t, cfg.GetCompanions())
}

func TestRemoveCompanion_UnknownNameIsASentinel(t *testing.T) {
	app, _, _ := companionHome(t)
	require.ErrorIs(t, RemoveCompanion(context.Background(), app, "acme"), ErrCompanionNotRegistered)
}

func TestListCompanions_ReportsWhetherEachResolves(t *testing.T) {
	_, _, bin := companionHome(t)
	got := ListCompanions([]string{"acme", "ghost"})
	assert.Equal(t, []CompanionListing{
		{Name: "acme", Bin: "ctxloom-companion-acme", Path: filepath.Join(bin, "ctxloom-companion-acme"), Resolves: true},
		{Name: "ghost", Bin: "ctxloom-companion-ghost"},
	}, got)
}
