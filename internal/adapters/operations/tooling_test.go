package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestCollectTooling_CollectsCompanionToolingDeclarations proves collection
// reads every admitted companion's TYPED `init.tooling` field, attributes it
// to the companion's source ref, and skips companions that declare none. The
// nil pipe exercises the real trust-gated exposure path.
func TestCollectTooling_CollectsCompanionToolingDeclarations(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk":      "init:\n  tooling: Install golangci-lint v2 and gofumpt.\n",
		"taskloom": "run:\n  version: 1.0.0\n",
	})
	appDir, _ := regenTestApp(t)
	cfg := published(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}))

	got := CollectTooling(cfg, nil)
	require.Len(t, got, 1, "only the companion declaring tooling is collected")
	assert.Equal(t, "ctxloom+companion:ltk", got[0].Source, "source is the companion's canonical ref")
	assert.Equal(t, "Install golangci-lint v2 and gofumpt.", got[0].Content)
}

// TestCollectTooling_MagicCommandNameNoLongerContributes pins the clean
// break: a command named `tooling` — in a project bundle or a companion's RUN
// loadout — is an ordinary command, not a tooling declaration.
func TestCollectTooling_MagicCommandNameNoLongerContributes(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk": "run:\n  version: 1.0.0\n  commands:\n    tooling:\n      content: COMPANION-MAGIC-COMMAND\n",
	})
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "go-tools", `version: "1.0"
commands:
  tooling:
    content: "PROJECT-MAGIC-COMMAND"
`)
	cfg := published(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}))

	assert.Empty(t, CollectTooling(cfg, nil), "a command named tooling is not a tooling declaration")
}

// TestCollectTooling_NilSafe: a nil config never errors — the
// pipeline is advisory and must not block anything.
func TestCollectTooling_NilSafe(t *testing.T) {
	assert.Nil(t, CollectTooling(nil, nil))
}

// TestScaffoldContainerBase_WritesEmbeddedAndWiresConfig: the scaffold
// materializes the embedded default base (content-identical to what the
// default auto-build was using) and persists isolation_base_containerfile,
// so the default build picks it up after a reload.
func TestScaffoldContainerBase_WritesEmbeddedAndWiresConfig(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	path, err := ScaffoldContainerBase(context.Background(), managerFor(t, appDir), cfg, "", false)
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(container.Base()), string(b), "the scaffold starts from the embedded default base")

	reloaded, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, path, reloaded.IsolationBaseContainerfilePath(),
		"isolation_base_containerfile survives the config round-trip")
}

// TestScaffoldContainerBase_AdoptsExistingFile: an existing file at the
// target is wired into config but its content is NEVER overwritten
// (WIP safety) — unless force.
func TestScaffoldContainerBase_AdoptsExistingFile(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(t, appDir)
	target := filepath.Join(cfg.GetAppRoot(), DefaultContainerBasePath)
	require.NoError(t, os.WriteFile(target, []byte("FROM my/custom:base\n"), 0o644))

	path, err := ScaffoldContainerBase(context.Background(), mgr, cfg, "", false)
	require.NoError(t, err)
	b, _ := os.ReadFile(path)
	assert.Equal(t, "FROM my/custom:base\n", string(b), "existing content adopted, not clobbered")

	_, err = ScaffoldContainerBase(context.Background(), mgr, cfg, "", true)
	require.NoError(t, err)
	b, _ = os.ReadFile(path)
	assert.Equal(t, string(container.Base()), string(b), "force rewrites from the embedded base")
}

// TestScaffoldContainerBase_AlreadyConfiguredIsNoOp: a config that already
// points at a base Containerfile is returned as-is — the user owns it.
func TestScaffoldContainerBase_AlreadyConfiguredIsNoOp(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\nisolation_base_containerfile: custom/base.Containerfile\n")

	path, err := ScaffoldContainerBase(context.Background(), managerFor(t, appDir), cfg, "", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfg.GetAppRoot(), "custom/base.Containerfile"), path)
	_, statErr := os.Stat(filepath.Join(cfg.GetAppRoot(), DefaultContainerBasePath))
	assert.True(t, os.IsNotExist(statErr), "no default-path file is written when a base is already configured")
}

// TestScaffoldContainerBase_RejectsPathTraversal: relPath rides in
// as a bare `--path` CLI flag with no containment check, so `--path
// ../../evil` joined onto GetAppRoot() escaped the project entirely. The
// call must error and nothing may land outside the project root.
func TestScaffoldContainerBase_RejectsPathTraversal(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	projectRoot := cfg.GetAppRoot()
	outsideMarker := filepath.Join(filepath.Dir(filepath.Dir(projectRoot)), "evil-base.Containerfile")
	_ = os.Remove(outsideMarker)

	_, err := ScaffoldContainerBase(context.Background(), managerFor(t, appDir), cfg, "../../evil-base.Containerfile", false)
	require.Error(t, err, "a relPath that escapes the project root must be rejected")

	_, statErr := os.Stat(outsideMarker)
	assert.True(t, os.IsNotExist(statErr), "nothing must be written outside the project root")
}

// TestScaffoldContainerBase_MaterializesWhenConfiguredPathIsMissing:
// isolation_base_containerfile can point at a path that was never
// actually created (deleted, a typo, checked out from a branch that doesn't
// ship it) — ScaffoldContainerBase used to return that path as "already
// configured" without ever checking it exists, so the CLI printed
// "Base Containerfile: <path>. Edit it..." for a file with zero bytes on
// disk. The configured location must actually be materialized.
func TestScaffoldContainerBase_MaterializesWhenConfiguredPathIsMissing(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\nisolation_base_containerfile: custom/base.Containerfile\n")
	target := filepath.Join(cfg.GetAppRoot(), "custom/base.Containerfile")
	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "precondition: the configured file does not exist yet")

	path, err := ScaffoldContainerBase(context.Background(), managerFor(t, appDir), cfg, "", false)
	require.NoError(t, err)
	assert.Equal(t, target, path)

	b, err := os.ReadFile(target)
	require.NoError(t, err, "the configured base Containerfile must actually be written, not just reported as present")
	assert.Equal(t, string(container.Base()), string(b))
}
