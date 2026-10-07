package operations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestCollectTooling_CollectsCompanionToolingDeclarations proves collection
// reads every registered companion's TYPED `init.tooling` field, attributes it
// to the companion's source ref, and skips companions that declare none. The
// nil pipe exercises the real trust-gated exposure path.
func TestCollectTooling_CollectsCompanionToolingDeclarations(t *testing.T) {
	testsupport.Isolate(t)
	fakeCompanions(t, map[string]string{
		"ltk":      "init:\n  tooling: Install golangci-lint v2 and gofumpt.\n",
		"taskloom": "run:\n  version: 1.0.0\n",
	})
	appDir, _ := regenTestApp(t)
	cfg := published(t, config.NewFixture(config.Fixture{AppPaths: []string{appDir}, Companions: fakedCompanionNames}))

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
	cfg := published(t, config.NewFixture(config.Fixture{AppPaths: []string{appDir}, Companions: fakedCompanionNames}))

	assert.Empty(t, CollectTooling(cfg, nil), "a command named tooling is not a tooling declaration")
}

// TestCollectTooling_NilSafe: a nil config never errors — the
// pipeline is advisory and must not block anything.
func TestCollectTooling_NilSafe(t *testing.T) {
	assert.Nil(t, CollectTooling(nil, nil))
}

// TestScaffoldDevcontainer_WritesADevcontainerOnTheEmbeddedBase: the
// scaffold writes .devcontainer/ — a devcontainer.json building the Dockerfile
// beside it, seeded from the embedded default base — which is exactly what an
// unset isolation_base then adopts as the agent image's base.
func TestScaffoldDevcontainer_WritesADevcontainerOnTheEmbeddedBase(t *testing.T) {
	cfg, _ := loadConfigDir(t, "schema_version: 7\n")

	dir, err := ScaffoldDevcontainer(cfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfg.GetAppRoot(), ".devcontainer"), dir)

	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Equal(t, string(container.Base()), string(dockerfile), "the Dockerfile starts from the embedded default base")

	raw, err := os.ReadFile(filepath.Join(dir, "devcontainer.json"))
	require.NoError(t, err)
	var dc struct {
		Build struct {
			Dockerfile string `json:"dockerfile"`
		} `json:"build"`
	}
	require.NoError(t, json.Unmarshal(raw, &dc))
	assert.Equal(t, "Dockerfile", dc.Build.Dockerfile, "devcontainer.json builds the Dockerfile beside it")
	assert.Equal(t, filepath.Join(dir, "devcontainer.json"), isolation.FindDevcontainerJSON(cfg.GetAppRoot()),
		"the scaffold lands where the image build looks for a devcontainer")
}

// TestScaffoldDevcontainer_RefusesAnExistingDevcontainer: a project that
// already has a devcontainer — in either canonical spelling, or any
// .devcontainer/ directory at all — is refused with a typed error naming it,
// and nothing is written.
func TestScaffoldDevcontainer_RefusesAnExistingDevcontainer(t *testing.T) {
	for name, plant := range map[string]func(root string) string{
		"devcontainer directory": func(root string) string {
			p := filepath.Join(root, ".devcontainer")
			require.NoError(t, os.MkdirAll(p, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(p, "Dockerfile"), []byte("FROM mine\n"), 0o644))
			return p
		},
		"root devcontainer.json": func(root string) string {
			p := filepath.Join(root, ".devcontainer.json")
			require.NoError(t, os.WriteFile(p, []byte(`{"image":"mine"}`), 0o644))
			return p
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := loadConfigDir(t, "schema_version: 7\n")
			existing := plant(cfg.GetAppRoot())

			_, err := ScaffoldDevcontainer(cfg)
			var exists *DevcontainerExistsError
			require.ErrorAs(t, err, &exists)
			assert.Equal(t, existing, exists.Path)
			assert.Contains(t, err.Error(), existing, "the refusal names what is already there")

			_, statErr := os.Stat(filepath.Join(cfg.GetAppRoot(), ".devcontainer", "devcontainer.json"))
			assert.True(t, os.IsNotExist(statErr), "nothing is written on refusal")
		})
	}
}
