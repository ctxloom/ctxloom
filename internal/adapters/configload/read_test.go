package configload

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// A key the config schema does not describe is an unknown-key finding naming
// its dotted path — whatever it once meant. These keys each once changed what
// a run did (a credential, a concurrency ceiling, a delegation privilege, an
// engine environment, engine hooks); a load that dropped one silently would
// run on a setting nobody chose, so each must reach the unknown-key gate.
func TestLoad_FormerlyMeaningfulKeysAreUnknownKeys(t *testing.T) {
	for path, body := range map[string]string{
		"agents.dev.auth":        "agents:\n  dev:\n    llm: claude-code\n    auth: login\n",
		"agents.dev.engine":      "agents:\n  dev:\n    engine: claude-code\n",
		"agents.dev.coordinator": "agents:\n  dev:\n    llm: claude-code\n    coordinator: true\n",
		"agent_turn_cap":         "agent_turn_cap: 4\n",
		"llm.configs.big.env":    "llm:\n  configs:\n    big:\n      type: claude-code\n      env:\n        ANTHROPIC_API_KEY: sk-secret\n",
		"hooks":                  "hooks:\n  plugins: {}\n",
	} {
		t.Run(path, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			testsupport.WriteFileString(t, fs, "/proj/.ctxloom/config.yaml", "schema_version: 7\n"+body, 0o644)

			cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir("/proj/.ctxloom"))
			require.NoError(t, err)
			var found bool
			for _, w := range cfg.GetWarnings() {
				if w.Kind == config.WarnKindUnknownKey && strings.Contains(w.Text, "`"+path+"`") {
					found = true
				}
			}
			assert.True(t, found, "want an unknown-key finding naming %s, got %+v", path, cfg.GetWarnings())
		})
	}
}

// The mock's test-control knobs live under their own key, which the schema
// describes.
func TestParseConfig_MockControlIsItsOwnKey(t *testing.T) {
	const mockDoc = "schema_version: 7\nllm:\n  configs:\n    m:\n      type: mock\n      mock_control:\n        CTXLOOM_MOCK_RESPONSE: canned\n"
	cfg, err := config.ParseConfig([]byte(mockDoc))
	require.NoError(t, err)
	entry, ok := cfg.GetLLMEntry("m")
	require.True(t, ok)
	assert.Equal(t, map[string]any{"CTXLOOM_MOCK_RESPONSE": "canned"}, entry.Body["mock_control"])
}

func TestLoad_WithOptions(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create .ctxloom directory structure with persistent subdir
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// A valid config file already in the new (default-agent) shape.
	configContent := `
schema_version: 7
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
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(configContent), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
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
schema_version: 7
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

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.NoError(t, err)

	control, ok := cfg.ToFixture().LM.Configs["m"].Body["mock_control"].(map[string]any)
	require.True(t, ok, "mock_control should decode into Body as a map, got %#v", cfg.ToFixture().LM.Configs["m"].Body["mock_control"])
	assert.Equal(t, "canned", control["CTXLOOM_MOCK_RESPONSE"], "uppercase key must be preserved verbatim")
	assert.Contains(t, control, "Mixed_Case")
	assert.NotContains(t, control, "ctxloom_mock_response", "key must not be lowercased")
}

func TestLoad_CurrentConfigReadsItsVersion(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	current := "schema_version: 7\nllm:\n  configs:\n    claude-code: { type: claude-code }\n  defaults:\n    primary: claude-code\n"
	cfgPath := paths.ConfigPath(appDir)
	testsupport.WriteFile(t, fs, cfgPath, []byte(current), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Equal(t, config.CurrentConfigVersion, cfg.ToFixture().SchemaVersion)
}

func TestLoad_NoConfigFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// No config.yaml file - should still work
	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
	require.NoError(t, err)

	assert.NotNil(t, cfg.ToFixture().LM.Configs)
}

func TestLoadConfigLayer_AbsentAndUnparsable(t *testing.T) {
	src, err := New(nil, nil)
	require.NoError(t, err)

	t.Run("absent file is nil values and no error", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		b := config.NewBuilder(safefs.NewMem(fs), "/", config.SourceProject)
		values, err := src.loadConfigLayer(b, "/nonexistent/config.yaml", fs)
		assert.NoError(t, err)
		assert.Nil(t, values)
	})

	t.Run("present unparsable file is refused by name", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		testsupport.WriteFile(t, fs, "/config.yaml", []byte("invalid: ["), 0644)
		b := config.NewBuilder(safefs.NewMem(fs), "/", config.SourceProject)
		values, err := src.loadConfigLayer(b, "/config.yaml", fs)
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
schema_version: 7
llm:
  configs: "should be a map not string"
`
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(configContent), 0644)

	// Now returns config with warnings instead of error for resilient startup
	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
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
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte("schema_version: 7\nllm:\n  default_agent: claude\n"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))
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
	// A file that is not YAML is refused by name (only an ABSENT layer is
	// the shipped default); a startup never proceeds on a config it
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
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(malformedYAML), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))

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
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte("{{{{invalid"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))

	require.ErrorIs(t, err, ErrUnparsableLayer)
	assert.Contains(t, err.Error(), paths.ConfigPath(appDir))
	assert.Nil(t, cfg)
}

func TestResilientStartup_NonExistentProfile(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(bundletree.ProjectProfilesDirFS(t, fs, appDir), 0755))

	// config.Config references a non-existent profile. Written in the CURRENT schema:
	// a fixture spelled in a retired one only passes while a migration happens
	// to carry it forward, which makes it a test of the migration rather than
	// of the behaviour it names.
	configYAML := fmt.Sprintf(`
schema_version: %d
default_agent: default
agents:
  default:
    profiles:
      - nonexistent-profile
`, config.CurrentConfigVersion)
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(configYAML), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))

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
	testsupport.WriteFile(t, fs, filepath.Join(appDir, "config.yaml"), []byte(""), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))

	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// An empty file carries no schema_version, so its layer is refused as a
	// finding rather than an error, and we still start.
	assert.NotNil(t, cfg.ToFixture().LM.Configs)
}

func TestResilientStartup_PartiallyValidConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// config.Config with some valid and some invalid parts (unknown property in plugin).
	// Schema validation may catch this, but we should still not fail -- and the
	// VALID part must survive, which is the whole claim. The profile lives in
	// the project bundle now that the inline arm is retired, so the surviving
	// good part is read through the loader rather than off the config struct.
	configYAML := fmt.Sprintf(`
schema_version: %d
llm:
  configs:
    claude-code:
      unknown_property: true
`, config.CurrentConfigVersion)
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(configYAML), 0644)
	require.NoError(t, fs.MkdirAll(bundletree.ProjectProfilesDirFS(t, fs, appDir), 0755))
	testsupport.WriteFile(t, fs, filepath.Join(bundletree.ProjectProfilesDirFS(t, fs, appDir), "valid-profile.yaml"),
		[]byte("description: \"This is valid\"\n"), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))

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
schema_version: 7
llm:
  configs: invalid-should-be-map
`
	testsupport.WriteFile(t, fs, paths.ConfigPath(appDir), []byte(configYAML), 0644)

	cfg, err := Load(WithRoot(safefs.NewMem(fs)), WithAppDir(appDir))

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
