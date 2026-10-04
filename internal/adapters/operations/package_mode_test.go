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
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestAssemblePackage_ARunnerFedPackageCarriesNoMailDrain: the package of a
// run its runner hands mail to (PackageRequest.Mail) declares no turn-start
// mail reader, end to end through the one package assembly (rows
// worried-chief F4, tacky-carload); the zero reader — every at-rest writer
// and the owner's launch — keeps it.
func TestAssemblePackage_ARunnerFedPackageCarriesNoMailDrain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})
	turnStart := func(reader sessions.MailReader) string {
		pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Mail: reader})
		require.NoError(t, err)
		var cmds []string
		for _, h := range pkg.Hooks.Unified.TurnStart {
			cmds = append(cmds, strings.Join(append([]string{h.Command}, h.Args...), " "))
		}
		return strings.Join(cmds, " ")
	}
	assert.Contains(t, turnStart(sessions.MailByHook), "hook mail-drain")
	assert.NotContains(t, turnStart(sessions.MailByRunner), "hook mail-drain")
}

// TestAssembler_HandsTheMailReaderToThePackage: the launch assembler builds
// the package request from the Selection, its mail reader included — the step
// between launch.Resolve and AssemblePackage.
// MUTATION — drop Mail from the request assembler.Assemble builds — turns
// this red.
func TestAssembler_HandsTheMailReaderToThePackage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	snap := &config.Snapshot{Config: gatedFixture(config.Fixture{AppPaths: []string{appDir}})}
	pkg, err := (&assembler{engines: engines.Registry()}).Assemble(context.Background(), snap, launch.Selection{Mail: sessions.MailByRunner})
	require.NoError(t, err)
	for _, h := range pkg.Hooks.Unified.TurnStart {
		assert.NotContains(t, strings.Join(append([]string{h.Command}, h.Args...), " "), "hook mail-drain")
	}
}
