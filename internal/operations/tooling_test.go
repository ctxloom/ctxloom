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
// every bundle's `tooling` skill (bare-name match, like the
// agent-setup override contract), attributes it to its source, and skips
// bundles without one. The nil loader exercises the real trust-gated
// exposure path over locally-authored (baseline-trusted) bundles.
func TestCollectTooling_CollectsDeclarations(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "go-tools", `version: "1.0"
skills:
  tooling:
    content: "Install golangci-lint v2 and gofumpt."
`)
	writeRegenBundle(t, appDir, "unrelated", `version: "1.0"
skills:
  something-else:
    content: "NOT TOOLING"
`)
	cfg := &config.Config{AppPaths: []string{appDir}}

	got := CollectTooling(cfg, nil)
	require.Len(t, got, 1, "only the tooling skill is collected")
	assert.Contains(t, got[0].Source, "#skills/tooling", "source is the bundle-qualified ref")
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

	path, err := ScaffoldContainerBase(cfg, "", false)
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
	cfg, _ := loadConfigDir(t, "version: 5\n")
	target := filepath.Join(cfg.AppRoot, DefaultContainerBasePath)
	require.NoError(t, os.WriteFile(target, []byte("FROM my/custom:base\n"), 0o644))

	path, err := ScaffoldContainerBase(cfg, "", false)
	require.NoError(t, err)
	b, _ := os.ReadFile(path)
	assert.Equal(t, "FROM my/custom:base\n", string(b), "existing content adopted, not clobbered")

	_, err = ScaffoldContainerBase(cfg, "", true)
	require.NoError(t, err)
	b, _ = os.ReadFile(path)
	assert.Equal(t, string(container.Base), string(b), "force rewrites from the embedded base")
}

// TestScaffoldContainerBase_AlreadyConfiguredIsNoOp: a config that already
// points at a base Containerfile is returned as-is — the user owns it.
func TestScaffoldContainerBase_AlreadyConfiguredIsNoOp(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 5\nisolation_base_containerfile: custom/base.Containerfile\n")

	path, err := ScaffoldContainerBase(cfg, "", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfg.AppRoot, "custom/base.Containerfile"), path)
	_, statErr := os.Stat(filepath.Join(cfg.AppRoot, DefaultContainerBasePath))
	assert.True(t, os.IsNotExist(statErr), "no default-path file is written when a base is already configured")
}
