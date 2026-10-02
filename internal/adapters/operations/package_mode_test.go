package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestAssemblePackage_AStructuredPackageCarriesNoMailDrain: the package a
// structured launch delivers (PackageRequest.Mode) declares no turn-start
// mail reader, end to end through the one package assembly (row
// worried-chief F4); the zero mode — every at-rest writer and the owner's
// interactive launch — keeps it.
func TestAssemblePackage_AStructuredPackageCarriesNoMailDrain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})
	turnStart := func(mode engine.Mode) string {
		pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Mode: mode})
		require.NoError(t, err)
		var cmds []string
		for _, h := range pkg.Hooks.Unified.TurnStart {
			cmds = append(cmds, strings.Join(append([]string{h.Command}, h.Args...), " "))
		}
		return strings.Join(cmds, " ")
	}
	assert.Contains(t, turnStart(engine.Interactive), "hook mail-drain")
	assert.NotContains(t, turnStart(engine.Structured), "hook mail-drain")
}

// TestAssembler_HandsTheLaunchModeToThePackage: the launch assembler builds
// the package request from the Selection, mode included — the step between
// launch.Resolve and AssemblePackage.
// MUTATION — drop Mode from the request assembler.Assemble builds — turns
// this red.
func TestAssembler_HandsTheLaunchModeToThePackage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	snap := &config.Snapshot{Config: gatedFixture(config.Fixture{AppPaths: []string{appDir}})}
	pkg, err := (&assembler{engines: engines.Registry()}).Assemble(context.Background(), snap, launch.Selection{Mode: engine.Structured})
	require.NoError(t, err)
	for _, h := range pkg.Hooks.Unified.TurnStart {
		assert.NotContains(t, strings.Join(append([]string{h.Command}, h.Args...), " "), "hook mail-drain")
	}
}
