package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestCollectTooling_CollectsDeclarations proves collection finds
// every bundle's `tooling` command (bare-name match, like the
// agent-setup override contract), attributes it to its source, and skips
// bundles without one. The nil loader exercises the real trust-gated
// exposure path over locally-authored (baseline-trusted) bundles.
func TestCollectTooling_CollectsDeclarations(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "go-tools", `version: "1.0"
commands:
  tooling:
    content: "Install golangci-lint v2 and gofumpt."
`)
	writeRegenBundle(t, appDir, "unrelated", `version: "1.0"
commands:
  something-else:
    content: "NOT TOOLING"
`)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	got := CollectTooling(cfg, nil)
	require.Len(t, got, 1, "only the tooling command is collected")
	assert.Contains(t, got[0].Source, "#commands/tooling", "source is the bundle-qualified ref")
	assert.Contains(t, got[0].Source, "go-tools", "source names the bundle")
	assert.Equal(t, "Install golangci-lint v2 and gofumpt.", got[0].Content)
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

	path, err := ScaffoldContainerBase(managerFor(appDir), cfg, "", false)
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(container.Base), string(b), "the scaffold starts from the embedded default base")

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, path, reloaded.IsolationBaseContainerfilePath(),
		"isolation_base_containerfile survives the config round-trip")
}

// TestScaffoldContainerBase_AdoptsExistingFile: an existing file at the
// target is wired into config but its content is NEVER overwritten
// (WIP safety) — unless force.
func TestScaffoldContainerBase_AdoptsExistingFile(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)
	target := filepath.Join(cfg.GetAppRoot(), DefaultContainerBasePath)
	require.NoError(t, os.WriteFile(target, []byte("FROM my/custom:base\n"), 0o644))

	path, err := ScaffoldContainerBase(mgr, cfg, "", false)
	require.NoError(t, err)
	b, _ := os.ReadFile(path)
	assert.Equal(t, "FROM my/custom:base\n", string(b), "existing content adopted, not clobbered")

	_, err = ScaffoldContainerBase(mgr, cfg, "", true)
	require.NoError(t, err)
	b, _ = os.ReadFile(path)
	assert.Equal(t, string(container.Base), string(b), "force rewrites from the embedded base")
}

// TestScaffoldContainerBase_AlreadyConfiguredIsNoOp: a config that already
// points at a base Containerfile is returned as-is — the user owns it.
func TestScaffoldContainerBase_AlreadyConfiguredIsNoOp(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\nisolation_base_containerfile: custom/base.Containerfile\n")

	path, err := ScaffoldContainerBase(managerFor(appDir), cfg, "", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfg.GetAppRoot(), "custom/base.Containerfile"), path)
	_, statErr := os.Stat(filepath.Join(cfg.GetAppRoot(), DefaultContainerBasePath))
	assert.True(t, os.IsNotExist(statErr), "no default-path file is written when a base is already configured")
}

// TestScaffoldContainerBase_RejectsPathTraversal: U088-F26. relPath rides in
// as a bare `--path` CLI flag with no containment check, so `--path
// ../../evil` joined onto GetAppRoot() escaped the project entirely. The
// call must error and nothing may land outside the project root.
func TestScaffoldContainerBase_RejectsPathTraversal(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	projectRoot := cfg.GetAppRoot()
	outsideMarker := filepath.Join(filepath.Dir(filepath.Dir(projectRoot)), "evil-base.Containerfile")
	_ = os.Remove(outsideMarker)

	_, err := ScaffoldContainerBase(managerFor(appDir), cfg, "../../evil-base.Containerfile", false)
	require.Error(t, err, "a relPath that escapes the project root must be rejected")

	_, statErr := os.Stat(outsideMarker)
	assert.True(t, os.IsNotExist(statErr), "nothing must be written outside the project root")
}

// TestScaffoldContainerBase_MaterializesWhenConfiguredPathIsMissing:
// U088-F04. isolation_base_containerfile can point at a path that was never
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

	path, err := ScaffoldContainerBase(managerFor(appDir), cfg, "", false)
	require.NoError(t, err)
	assert.Equal(t, target, path)

	b, err := os.ReadFile(target)
	require.NoError(t, err, "the configured base Containerfile must actually be written, not just reported as present")
	assert.Equal(t, string(container.Base), string(b))
}
