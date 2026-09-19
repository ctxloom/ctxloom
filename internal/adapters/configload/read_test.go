package configload

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/configload/layerscope"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoad_RetiredAgentTurnCapKeyRefusedNotIgnored pins the load-bearing half
// of the agent_turn_cap -> delegation.concurrency rename: a config still
// carrying the retired flat key must FAIL LOUD, naming the new key — never
// silently drop the setting back to the built-in default. This decode path
// (loadLayeredConfig's merged-layer Unmarshal) is lenient (no KnownFields),
// so without this explicit check an untouched `agent_turn_cap:` would be
// dropped in silence.
//
// Load() itself is fault-tolerant by this package's own design (every load
// fault, this one included, downgrades to a recorded config.Warning rather than a
// returned error — see decodeMergedLayers and warnings.go's "EVERY kind
// declared below is fatal-class in strict mode"): the actual fail-loud
// enforcement is the STRICT-MODE gate a caller runs over cfg.GetWarnings()
// (config.RecordWarnings + strictness.FindingsError), not Load's own return
// value. So this test asserts what Load() actually contracts: cfg still
// loads (never nil), but carries a warning whose text names BOTH the
// retired key and its replacement — the exact text a fatal-class finding
// surfaces to a user under that gate.
func TestLoad_RetiredAgentTurnCapKeyRefusedNotIgnored(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom/config.yaml", []byte("version: 6\nagent_turn_cap: 3\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir("/proj/.ctxloom"))
	require.NoError(t, err)
	require.NotNil(t, cfg)

	var found *config.Warning
	for _, w := range cfg.GetWarnings() {
		if strings.Contains(w.Text, "agent_turn_cap") {
			found = &w
		}
	}
	require.NotNil(t, found, "a config carrying the retired key must record a warning naming it, not silently ignore it: %+v", cfg.GetWarnings())
	assert.Contains(t, found.Text, "delegation.concurrency", "the warning must name the CURRENT key, not just reject the old one")
}

// TestLoad_RetiredLLMEnvKeyRefusedNotIgnored pins the retirement of
// llm.configs.<label>.env: ctxloom no longer carries an engine's environment
// or credentials in its config at all (every engine authenticates itself
// from the ambient environment, and the launched process inherits it), so a
// config still spelling the key must FAIL LOUD and name the replacement —
// never decode into a dead Body key that nothing reads, which would leave a
// user believing their variable reached the engine.
//
// Same contract as TestLoad_RetiredAgentTurnCapKeyRefusedNotIgnored: Load()
// records the refusal as a fatal-class config.Warning naming both the retired key
// and its replacement; config.ParseConfig (the init path, which returns decode
// errors outright) surfaces the sentinel itself.
func TestLoad_RetiredLLMEnvKeyRefusedNotIgnored(t *testing.T) {
	const doc = "version: 6\nllm:\n  configs:\n    big:\n      type: claude-code\n      env:\n        ANTHROPIC_API_KEY: sk-secret\n"

	t.Run("Load records a fatal-class warning naming the key, the label and the replacement", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		testsupport.WriteFileString(t, fs, "/proj/.ctxloom/config.yaml", doc, 0644)

		cfg, err := Load(WithFS(fs), WithAppDir("/proj/.ctxloom"))
		require.NoError(t, err)
		require.NotNil(t, cfg)

		var found *config.Warning
		for _, w := range cfg.GetWarnings() {
			if strings.Contains(w.Text, config.ErrRetiredLLMEnvKey.Error()) {
				found = &w
			}
		}
		require.NotNil(t, found, "a config carrying llm.configs.<label>.env must record the refusal, not silently ignore it: %+v", cfg.GetWarnings())
		assert.Contains(t, found.Text, `"big"`, "the refusal must name the label carrying the key")
		assert.Contains(t, found.Text, "ambient environment", "the refusal must name the replacement, not just reject the key")
		_, decoded := cfg.GetLLMEntry("big")
		assert.False(t, decoded, "a refused document must not half-decode into a label whose env silently went nowhere")
	})

	t.Run("config.ParseConfig returns the sentinel", func(t *testing.T) {
		_, err := config.ParseConfig([]byte(doc))
		require.ErrorIs(t, err, config.ErrRetiredLLMEnvKey)
	})

	t.Run("the mock's control channel is not the retired key", func(t *testing.T) {
		const mockDoc = "version: 6\nllm:\n  configs:\n    m:\n      type: mock\n      mock_control:\n        CTXLOOM_MOCK_RESPONSE: canned\n"
		cfg, err := config.ParseConfig([]byte(mockDoc))
		require.NoError(t, err)
		entry, ok := cfg.GetLLMEntry("m")
		require.True(t, ok)
		assert.Equal(t, map[string]any{"CTXLOOM_MOCK_RESPONSE": "canned"}, entry.Body["mock_control"])
	})
}

func TestLoad_WithOptions(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create .ctxloom directory structure with persistent subdir
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// A valid config file already in the new (default-agent) shape.
	configContent := `
version: 6
llm:
  configs:
    claude-code: { type: claude-code }
  defaults:
    primary: claude-code
default_agent: dev
agents:
  dev:
    profiles: [test]
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configContent), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	assert.Equal(t, []string{"test"}, cfg.DefaultAgentProfiles())
	assert.Equal(t, "claude-code", cfg.ToFixture().LM.Defaults.Primary)
	assert.Equal(t, []string{appDir}, cfg.ToFixture().AppPaths)
	assert.Equal(t, appDir, cfg.ToFixture().AppDir)
	assert.Equal(t, config.SourceProject, cfg.ToFixture().Source)
}

// TestLoad_PreservesBodyMapKeyCase is a regression guard: the Load path must
// not lowercase case-sensitive keys inside a backend's polymorphic Body. The
// previous decoder (viper) lowercased every key, so a `SOME_KEY` inside a
// label's map reached the launched process as `some_key` and the engine never
// saw it. config.ParseConfig (init) was always correct, which masked the divergence.
// The mock's control map is the case-sensitive Body map that survives today.
func TestLoad_PreservesBodyMapKeyCase(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	configContent := `
version: 3
llm:
  configs:
    m:
      type: mock
      mock_control:
        CTXLOOM_MOCK_RESPONSE: canned
        Mixed_Case: x
  defaults:
    primary: m
`
	testsupport.WriteFileString(t, fs, paths.ConfigPath(appDir), configContent, 0644)

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	control, ok := cfg.ToFixture().LM.Configs["m"].Body["mock_control"].(map[string]any)
	require.True(t, ok, "mock_control should decode into Body as a map, got %#v", cfg.ToFixture().LM.Configs["m"].Body["mock_control"])
	assert.Equal(t, "canned", control["CTXLOOM_MOCK_RESPONSE"], "uppercase key must be preserved verbatim")
	assert.Contains(t, control, "Mixed_Case")
	assert.NotContains(t, control, "ctxloom_mock_response", "key must not be lowercased")
}

func TestLoad_CurrentConfigHasNoPendingUpgrade(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	current := "version: 6\nllm:\n  configs:\n    claude-code: { type: claude-code }\n  defaults:\n    primary: claude-code\n"
	cfgPath := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(fs, cfgPath, []byte(current), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Nil(t, cfg.ToFixture().PendingUpgrade, "a current-version config must not record a pending upgrade")
	assert.Equal(t, config.CurrentConfigVersion, cfg.ToFixture().Version)
}

func TestLoad_NoConfigFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// No config.yaml file - should still work
	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	assert.NotNil(t, cfg.ToFixture().LM.Configs)
}

func TestLoadConfigLayer_AbsentAndUnparsable(t *testing.T) {
	src, err := New(nil, nil)
	require.NoError(t, err)

	t.Run("absent file is nil values and no error", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		b := config.NewBuilder(fs, true, "/", config.SourceProject)
		values, pending, err := src.loadConfigLayer(b, layerscope.LayerProject, "/", "", "/nonexistent/config.yaml", fs)
		assert.NoError(t, err)
		assert.Nil(t, values)
		assert.Nil(t, pending)
	})

	t.Run("present unparsable file is refused by name", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte("invalid: ["), 0644))
		b := config.NewBuilder(fs, true, "/", config.SourceProject)
		values, _, err := src.loadConfigLayer(b, layerscope.LayerProject, "/", "", "/config.yaml", fs)
		require.ErrorIs(t, err, ErrUnparsableLayer)
		assert.Contains(t, err.Error(), "/config.yaml")
		assert.Nil(t, values)
	})
}

func TestLoad_SchemaValidationProducesWarning(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Create config that fails schema validation (using wrong type)
	configContent := `
llm:
  configs: "should be a map not string"
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configContent), 0644))

	// Now returns config with warnings instead of error for resilient startup
	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// Should have collected warnings about parse/validation issues
	assert.NotEmpty(t, cfg.GetWarnings())
}

// A schema-COMPILE failure (as opposed to a document that fails validation
// against a good schema) used to degrade to "everything is valid" —
// zap-only, invisible to cfg.GetWarnings() and therefore invisible to the
// strict-startup gate, which keys exclusively on that slice. Force the
// compile step itself to fail via the newConfigValidator seam (the real
// embedded schema cannot be made to fail without corrupting a build
// artifact) and assert the failure is now a fatal-class warning.
func TestLoad_SchemaCompileFailureProducesWarning(t *testing.T) {
	orig := newConfigValidator
	newConfigValidator = func() (*schema.ConfigValidator, error) {
		return nil, fmt.Errorf("simulated schema compile failure")
	}
	defer func() { newConfigValidator = orig }()

	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("llm:\n  default_agent: claude\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	assert.NoError(t, err, "a compile failure must degrade to a warning, not abort Load")
	require.NotNil(t, cfg)

	var found bool
	for _, w := range cfg.GetWarnings() {
		if w.Kind == config.WarnKindValidate && strings.Contains(w.Text, "schema failed to compile") {
			found = true
		}
	}
	assert.True(t, found, "a schema-compile failure must surface as a fatal-class (config.WarnKindValidate) warning so the strict-startup gate can see it; warnings: %v", cfg.GetWarnings())
}

func TestResilientStartup_MalformedConfig(t *testing.T) {
	// A file that is not YAML is refused by name (Part 1.8: only an ABSENT
	// layer is the shipped default); a startup never proceeds on a config it
	// could not read.
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	malformedYAML := `
llm:
  configs:
    - this is wrong format
    claude-code: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(malformedYAML), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	require.ErrorIs(t, err, ErrUnparsableLayer)
	assert.Contains(t, err.Error(), paths.ConfigPath(appDir))
	assert.Nil(t, cfg)
}

func TestResilientStartup_CompletelyInvalidYAML(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Completely unparseable YAML: refused by name, before any schema
	// judgement — a file that is not YAML has nothing to validate.
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("{{{{invalid"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	require.ErrorIs(t, err, ErrUnparsableLayer)
	assert.Contains(t, err.Error(), paths.ConfigPath(appDir))
	assert.Nil(t, cfg)
}

func TestResilientStartup_NonExistentProfile(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(appDir), 0755))

	// config.Config references a non-existent profile. Written in the CURRENT schema:
	// a fixture spelled in a retired one only passes while a migration happens
	// to carry it forward, which makes it a test of the migration rather than
	// of the behaviour it names.
	configYAML := fmt.Sprintf(`
version: %d
default_agent: default
agents:
  default:
    profiles:
      - nonexistent-profile
`, config.CurrentConfigVersion)
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configYAML), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	// Loading should succeed. The legacy defaults.profiles upgrades through the
	// v1→…→v6 chain into the synthesized default agent's profiles.
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, "default", cfg.ToFixture().DefaultAgent)

	// DefaultAgentProfiles returns the name even if the profile doesn't exist.
	defaults := cfg.DefaultAgentProfiles()
	assert.Contains(t, defaults, "nonexistent-profile")
}

func TestResilientStartup_EmptyConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Empty config file - schema validation will warn but not fail
	require.NoError(t, afero.WriteFile(fs, filepath.Join(appDir, "config.yaml"), []byte(""), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// Schema validation warns on empty config, but we still start
	assert.NotNil(t, cfg.ToFixture().LM.Configs)
}

func TestResilientStartup_PartiallyValidConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// config.Config with some valid and some invalid parts (unknown property in plugin).
	// Schema validation may catch this, but we should still not fail -- and the
	// VALID part must survive, which is the whole claim. The profile lives in
	// .ctxloom/profiles/ now that the inline arm is retired, so the surviving
	// good part is read through the loader rather than off the config struct.
	configYAML := fmt.Sprintf(`
version: %d
llm:
  configs:
    claude-code:
      unknown_property: true
`, config.CurrentConfigVersion)
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configYAML), 0644))
	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(appDir), 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(paths.ProfilesPath(appDir), "valid-profile.yaml"),
		[]byte("description: \"This is valid\"\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	loaded, lerr := cfg.GetProfileLoader().Load("valid-profile")
	require.NoError(t, lerr, "the valid profile must survive a partially-invalid config")
	assert.Equal(t, "This is valid", loaded.Description,
		"and survive with its CONTENT, not merely as a name in a map")
}

func TestResilientStartup_WarningsAreCollected(t *testing.T) {
	// Test that schema validation warnings are collected
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Create config with type mismatch that schema validation should catch
	configYAML := `
llm:
  configs: invalid-should-be-map
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configYAML), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	// Should not error, should have warnings
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// The config struct is valid even if content is wrong
}

// TestCtxloomProduct_NilValidatorLeavesKnownPathNil pins the degradation
// confload's Product doc describes: "Nil is treated as 'no schema knowledge
// available'". That branch is guarded by `if p.KnownPath != nil`, and a
// METHOD VALUE on a nil pointer is never a nil func — so passing
// validator.KnownPath unconditionally made the documented path unreachable
// from this product, no matter how the schema failed. The resolved config is
// the same either way (a predicate answering false for everything and an
// absent predicate both land on case 4), which is exactly why nothing else
// would ever notice.
func TestCtxloomProduct_NilValidatorLeavesKnownPathNil(t *testing.T) {
	assert.Nil(t, (&Sources{}).product().KnownPath,
		"no schema means no schema knowledge — confload's nil branch must be reachable")

	validator, err := newConfigValidator()
	require.NoError(t, err, "the real embedded schema must compile, or the other half of this test proves nothing")
	assert.NotNil(t, (&Sources{validator: validator}).product().KnownPath,
		"a compiled schema must still be handed through, or the nil case above is vacuous")
}
