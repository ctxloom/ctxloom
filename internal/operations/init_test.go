package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

func TestInitializeProject(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"

	res, err := InitializeProject(context.Background(), InitializeProjectRequest{AppDir: appDir, Engine: "claude-code", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "initialized", res.Status)

	cfgData, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
	require.NoError(t, err, "config.yaml should be written")
	body := string(cfgData)
	// The written config carries a v3 registry, not the old `llm.default` scalar.
	assert.Contains(t, body, "configs:")
	assert.Contains(t, body, "type: claude-code")
	assert.Contains(t, body, "primary: claude-code")
	assert.Contains(t, body, "fast: claude-fast")
	// `role` is registry-only metadata stripped on write — it must never persist.
	assert.NotContains(t, body, "role:")

	bundlesDir, err := afero.DirExists(fs, paths.LocalBundlesPath(appDir))
	require.NoError(t, err)
	assert.True(t, bundlesDir, "bundles dir should be created")

	remotesExists, err := afero.Exists(fs, paths.RemotesPath(appDir))
	require.NoError(t, err)
	assert.True(t, remotesExists, "remotes.yaml should be written")
}

// TestInitializeProject_UnknownEngineRefusesAndWritesNothing pins the fix for
// the "exit 0 + success message + corrupt config" defect: `req.Engine` naming
// a backend the registry doesn't know (a typo, an unsupported name) used to
// scaffold anyway via fallbackRegistry's `{type: <whatever>}`, producing a
// config.yaml that then failed ctxloom's own JSON-schema validation on the
// very next command. It must instead refuse loud, naming the bad value and
// the valid set, and must leave NOTHING behind — not even the directory tree
// MkdirAll would otherwise have created.
func TestInitializeProject_UnknownEngineRefusesAndWritesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"

	res, err := InitializeProject(context.Background(), InitializeProjectRequest{AppDir: appDir, Engine: "bogus", FS: fs})
	require.Error(t, err, "an unknown engine must be refused, not silently scaffolded")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), `"bogus"`, "the refusal must name the offending value")
	assert.Contains(t, err.Error(), "claude-code", "the refusal must list the valid engine set")

	exists, existsErr := afero.DirExists(fs, appDir)
	require.NoError(t, existsErr)
	assert.False(t, exists, "a rejected engine must leave no directory behind, not even an empty one")

	cfgExists, cfgErr := afero.Exists(fs, paths.ConfigPath(appDir))
	require.NoError(t, cfgErr)
	assert.False(t, cfgExists, "a rejected engine must leave no config.yaml behind")
}

// TestInitializeProject_DirtyTreeHandlerAnswerWritesBothKeys proves the init
// interview's single dirty-tree question actually LANDS both its answers ON
// DISK — not just that InitializeProject returns success. This is the exact
// silent-no-op shape this project is known for (exit 0, success message, zero
// bytes delivered). dirty_tree_handler still lands in config.yaml
// (config_save.go's applyConfigSections used to omit it regardless of what
// was passed in); the commit acknowledgement now lands in
// paths.DirtyTreeCommitAckPath — a SEPARATE file outside the layered config
// chain entirely (see config.SetDirtyTreeCommitAck's doc) — so this test
// reads each answer through its own independent path: raw config.yaml bytes
// plus config.ParseConfig for the handler, and config.DirtyTreeCommitAcknowledged
// (a fresh Lookup against the on-disk admission store, not any in-memory
// state InitializeProject might have retained) for the ack.
func TestInitializeProject_DirtyTreeHandlerAnswerWritesBothKeys(t *testing.T) {
	tests := []struct {
		name               string
		dirtyTreeHandler   string
		dirtyTreeCommitAck bool
		wantHandlerKeyOnly bool // true: assert the raw "dirty_tree_handler: <value>" line is present
		wantAcknowledged   bool // true: assert config.DirtyTreeCommitAcknowledged reports true
	}{
		{
			name:             "commit answer writes handler commit and records the ack",
			dirtyTreeHandler: "commit", dirtyTreeCommitAck: true,
			wantHandlerKeyOnly: true, wantAcknowledged: true,
		},
		{
			name:             "copy answer writes handler copy and records no ack",
			dirtyTreeHandler: "copy", dirtyTreeCommitAck: false,
			wantHandlerKeyOnly: true, wantAcknowledged: false,
		},
		{
			name:             "stale answer writes handler stale and records no ack",
			dirtyTreeHandler: "stale", dirtyTreeCommitAck: false,
			wantHandlerKeyOnly: true, wantAcknowledged: false,
		},
		{
			name:             "fail answer writes handler fail and records no ack",
			dirtyTreeHandler: "fail", dirtyTreeCommitAck: false,
			wantHandlerKeyOnly: true, wantAcknowledged: false,
		},
		{
			// The question never having run (flag-selected engine,
			// --non-interactive) must reproduce the pre-interview shape
			// exactly: neither answer recorded, so an existing project loads
			// the built-in "commit" default, unacknowledged (still refused).
			name:             "unanswered (zero values) writes neither key",
			dirtyTreeHandler: "", dirtyTreeCommitAck: false,
			wantHandlerKeyOnly: false, wantAcknowledged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			appDir := "/proj/.ctxloom"

			res, err := InitializeProject(context.Background(), InitializeProjectRequest{
				AppDir:             appDir,
				Engine:             "claude-code",
				DirtyTreeHandler:   tt.dirtyTreeHandler,
				DirtyTreeCommitAck: tt.dirtyTreeCommitAck,
				FS:                 fs,
			})
			require.NoError(t, err)
			require.Equal(t, "initialized", res.Status)

			// Independent read #1: raw bytes off disk, byte-level payload check.
			cfgData, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
			require.NoError(t, err, "config.yaml should be written")
			body := string(cfgData)
			if tt.wantHandlerKeyOnly {
				assert.Contains(t, body, "dirty_tree_handler: "+tt.dirtyTreeHandler)
			} else {
				assert.NotContains(t, body, "dirty_tree_handler:")
			}
			assert.NotContains(t, body, "dirty_tree_commit_ack", "the ack must never land in config.yaml — it belongs in the state store")

			// Independent read #2: parse those same bytes through the config
			// package's real loader and assert via its typed accessor — the
			// same GetDirtyTreeHandler the delegate gate itself reads, so this
			// proves the payload round-trips usably, not just that the
			// substring happens to appear.
			cfg, err := config.ParseConfig(cfgData)
			require.NoError(t, err)
			assert.Equal(t, tt.dirtyTreeHandler, cfg.GetDirtyTreeHandler())

			// Independent read #3: the ack, from its OWN store, via the exact
			// accessor operations.commitDirtyTree consults.
			assert.Equal(t, tt.wantAcknowledged, config.DirtyTreeCommitAcknowledged(fs, appDir))
		})
	}
}

func TestInitializeProject_RequiresAppDir(t *testing.T) {
	_, err := InitializeProject(context.Background(), InitializeProjectRequest{Engine: "claude-code", FS: afero.NewMemMapFs()})
	require.Error(t, err)
}

// TestInitializeProject_ScaffoldsSeedProfileAndDefaultAgent proves init's shape:
// a LOCAL default coding profile is scaffolded (inheriting ctxloom-default
// content) AND bound as the always-bound default agent (agents.default +
// default_agent, carrying the selected engine). profiles.defaults was retired,
// so the default context is now whatever the default agent composes.
func TestInitializeProject_ScaffoldsSeedProfileAndDefaultAgent(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	_, err := InitializeProject(context.Background(), InitializeProjectRequest{AppDir: appDir, Engine: "claude-code", FS: fs})
	require.NoError(t, err)

	// The local default coding profile file is written.
	profilePath := filepath.Join(paths.ProfilesPath(appDir), SeedProfileName+".yaml")
	data, err := afero.ReadFile(fs, profilePath)
	require.NoError(t, err, "seed profile should be scaffolded locally")
	body := string(data)
	assert.Contains(t, body, "ctxloom/ctxloom-default@bundles/", "seed profile wires ctxloom-default bundle content")
	assert.Contains(t, body, "#profiles/", "seed profile inherits ctxloom-default bundle profiles")
	assert.Contains(t, body, "parents:", "seed profile inherits remote baseline content")

	// The config binds the LOCAL profile as the default AGENT and points
	// default_agent at it, carrying the selected engine.
	cfgData, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
	require.NoError(t, err)
	assert.Contains(t, string(cfgData), "default_agent: default", "init writes the default_agent key")
	assert.Contains(t, string(cfgData), "agents:", "init seeds the default agent block")
	cfg, err := config.ParseConfig(cfgData)
	require.NoError(t, err)
	assert.Equal(t, SeedProfileName, cfg.GetDefaultAgent(), "default_agent names the seeded agent")
	assert.Equal(t, []string{SeedProfileName}, cfg.DefaultAgentProfiles(), "the default agent composes the local seed profile")
	require.Contains(t, cfg.GetConfiguredAgents(), SeedProfileName, "init seeds the default agent")
	assert.Equal(t, "claude-code", cfg.GetConfiguredAgents()[SeedProfileName].LLM, "the default agent carries the selected primary engine")
	assert.Equal(t, "host", cfg.GetConfiguredAgents()[SeedProfileName].Runtime)
}

// TestScaffoldSeedProfile_WriteIfAbsent proves a re-init does not clobber a
// default profile the user has since edited (profiles are user content, unlike
// the config/remotes scaffolding which is overwritten).
func TestScaffoldSeedProfile_WriteIfAbsent(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	profilePath := filepath.Join(paths.ProfilesPath(appDir), SeedProfileName+".yaml")
	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(appDir), 0755))
	require.NoError(t, afero.WriteFile(fs, profilePath, []byte("# my edits\n"), 0644))

	_, err := InitializeProject(context.Background(), InitializeProjectRequest{AppDir: appDir, Engine: "claude-code", FS: fs})
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, profilePath)
	require.NoError(t, err)
	assert.Equal(t, "# my edits\n", string(data), "an existing seed profile must not be overwritten")
}

func TestBuildInitialConfig(t *testing.T) {
	tests := []struct {
		name        string
		engine      string
		wantPrimary string // primary label → backend/model
		wantBackend string
		wantModel   string
		wantFast    string // fast label → backend/model
		wantFastBE  string
		wantFastMod string
	}{
		{
			name: "claude-code wires opus + haiku", engine: "claude-code",
			wantPrimary: "claude-code", wantBackend: "claude-code", wantModel: "claude-opus-4-8",
			wantFast: "claude-fast", wantFastBE: "claude-code", wantFastMod: "claude-haiku-4-5-20251001",
		},
		{
			// The shipped registry pins no model for antigravity: agy's own
			// configured default applies, and the lone role-marked entry
			// serves both the primary and fast roles.
			name: "antigravity wires its single role-marked entry to both roles", engine: "antigravity",
			wantPrimary: "antigravity", wantBackend: "antigravity", wantModel: "",
			wantFast: "antigravity", wantFastBE: "antigravity", wantFastMod: "",
		},
		{
			name: "engine without role markers falls back to a single entry", engine: "codex",
			wantPrimary: "codex", wantBackend: "codex", wantModel: "",
			wantFast: "codex", wantFastBE: "codex", wantFastMod: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := BuildInitialConfig(tt.engine, "")
			require.NoError(t, err)

			// `role` is registry-only — it must be stripped from the written config.
			assert.NotContains(t, string(data), "role:")

			cfg, err := config.ParseConfig(data)
			require.NoError(t, err)

			assert.Equal(t, tt.wantPrimary, cfg.GetLMConfig().Defaults.Primary)
			assert.Equal(t, tt.wantFast, cfg.GetLMConfig().Defaults.Fast)

			// Round-tripped config resolves the expected backend + model per role.
			be, model := cfg.ResolveLLM(cfg.PrimaryLabel())
			assert.Equal(t, tt.wantBackend, be)
			assert.Equal(t, tt.wantModel, model)
			assert.Equal(t, tt.wantBackend, cfg.GetDefaultLLM())
			assert.Equal(t, tt.wantModel, cfg.GetDefaultLLMModel())

			assert.Equal(t, tt.wantFastBE, cfg.GetCompactionLLM())
			assert.Equal(t, tt.wantFastMod, cfg.GetCompactionModel())
		})
	}
}
