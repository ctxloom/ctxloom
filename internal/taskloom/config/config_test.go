package config

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/resources"
)

// writeConfig writes a taskloom .taskloom/config.yaml under dir.
func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	full := filepath.Join(dir, DirName, FileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

// homingFlagSet builds a *pflag.FlagSet carrying taskloom's own --homing and
// --config-set flags, mirroring cmd/taskloom/root.go's registration.
func homingFlagSet(t *testing.T) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String(HomingFlagName, "", "")
	fs.StringArray(confload.ConfigSetFlagName, nil, "")
	return fs
}

// TestConfig_ProjectOverridesHome proves the project .taskloom/config.yaml
// beats the home one for taskloom's own `homing` key -- the precedence
// confload.Merge documents, exercised end to end through this package's own
// Load, not just confload directly.
func TestConfig_ProjectOverridesHome(t *testing.T) {
	home := taskstest.Isolate(t)
	writeConfig(t, home, "homing: home\n")

	project := t.TempDir()
	writeConfig(t, project, "homing: repo\n")

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing, "project config must win over home config")
}

// TestConfig_HomeOnlyStillApplies proves a home-only config (no project
// layer at all) is still honored -- a project with no .taskloom/config.yaml
// of its own inherits the home default.
func TestConfig_HomeOnlyStillApplies(t *testing.T) {
	home := taskstest.Isolate(t)
	writeConfig(t, home, "homing: home\n")

	project := t.TempDir()
	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "home", cfg.Homing)
}

// TestConfig_ExplicitFalseBeatsInheritedTrue proves taskloom's OWN
// confload.Product wiring (DirName/FileName/EnvPrefix, home/project Sources
// construction) preserves confload's D3 rule -- presence in the higher layer
// wins regardless of truthiness -- not just confload in the abstract
// (already proven generically by TestConfload_SecondProductReusesPattern).
// It exercises loadRaw directly with a synthetic "enabled" key outside
// taskloom's real (single-key) schema, since Load's schema validation would
// otherwise reject an unrecognized key before this precedence question is
// even reached.
func TestConfig_ExplicitFalseBeatsInheritedTrue(t *testing.T) {
	home := taskstest.Isolate(t)
	writeConfig(t, home, "enabled: true\n")

	project := t.TempDir()
	writeConfig(t, project, "enabled: false\n")

	merged, err := loadRaw(project, nil)
	require.NoError(t, err)
	assert.Equal(t, false, merged["enabled"], "project's explicit false must beat home's inherited true")
}

// TestConfig_UnknownKeyFailsLoud mirrors internal/config's unknown-key drift
// gate (see internal/config/unknown_keys.go's doc) for taskloom's own,
// much smaller schema: a key the schema does not recognize (additionalProperties:
// false at the top level) must not validate silently.
func TestConfig_UnknownKeyFailsLoud(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "bogus_key: true\n")

	_, err := Load(project, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "taskloom: config error")
}

// TestConfig_KnownKeyValidates is TestConfig_UnknownKeyFailsLoud's positive
// case: the one real schema key must not itself be rejected.
func TestConfig_KnownKeyValidates(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "homing: repo\n")

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing)
}

// TestConfig_EnvOverridesFile proves TASKLOOM_CONFIG_HOMING overrides both
// file layers.
func TestConfig_EnvOverridesFile(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "homing: home\n")
	t.Setenv("TASKLOOM_CONFIG_HOMING", "repo")

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing)
}

// TestConfig_ConfigSetOverridesEnv proves --config-set beats
// TASKLOOM_CONFIG_HOMING, completing the file<env<flag chain at the
// confload layer (ResolveMode's dedicated --homing flag is the OUTERMOST
// layer on top of this -- see TestHoming_FlagOverridesConfig).
func TestConfig_ConfigSetOverridesEnv(t *testing.T) {
	project := taskstest.ProjectDir(t)
	t.Setenv("TASKLOOM_CONFIG_HOMING", "home")

	fs := homingFlagSet(t)
	require.NoError(t, fs.Set(confload.ConfigSetFlagName, "homing=repo"))

	cfg, err := Load(project, fs)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing)
}

// TestHoming_RepoHomedWritesInsideRepo pins the repo-homed on-disk location:
// .taskloom/tasks.jsonl inside the project.
func TestHoming_RepoHomedWritesInsideRepo(t *testing.T) {
	got, err := paths.TasksLogPath(paths.ModeRepo, "/proj", "")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".taskloom", "tasks.jsonl"), got)
}

// TestHoming_HomeHomedWritesUnderHome pins today's (pre-homing) on-disk
// location unchanged.
func TestHoming_HomeHomedWritesUnderHome(t *testing.T) {
	home := taskstest.Isolate(t)
	got, err := paths.TasksLogPath(paths.ModeHome, "", "swift-amber-falcon")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".ctxloom", "tasks", "swift-amber-falcon.jsonl"), got)
}

// TestHoming_FlagOverridesConfig proves the dedicated --homing flag beats
// even a fully-resolved config value -- the outermost layer of the
// documented precedence chain (home < project < env < CLI flag).
func TestHoming_FlagOverridesConfig(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "homing: home\n")

	mode, err := ResolveMode(project, nil, "repo")
	require.NoError(t, err)
	assert.Equal(t, paths.ModeRepo, mode)
}

// TestHoming_EnvOverridesConfigButNotFlag proves the full four-layer chain in
// one shot: file < env < flag, all three represented at once.
func TestHoming_EnvOverridesConfigButNotFlag(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "homing: home\n")
	t.Setenv("TASKLOOM_CONFIG_HOMING", "repo")

	// No flag: env (repo) beats the file (home).
	mode, err := ResolveMode(project, nil, "")
	require.NoError(t, err)
	assert.Equal(t, paths.ModeRepo, mode, "env must beat the config file")

	// Flag given: flag (home) beats env (repo).
	mode, err = ResolveMode(project, nil, "home")
	require.NoError(t, err)
	assert.Equal(t, paths.ModeHome, mode, "the dedicated flag must beat env")
}

// TestConfig_AbsentEverywhereDefaultsToHome is the default-resolution core
// test: nothing at any layer (no home config, no project config, no env, no
// flag) sets the homing mode -- ResolveMode must silently resolve to
// paths.ModeHome, with NO error. ModeHome is the pre-homing status quo, so
// defaulting to it is a no-op for every project that predates this feature
// (including this repo, which ships no .taskloom/config.yaml); see the
// package doc for why only THIS direction is safe to default without asking.
func TestConfig_AbsentEverywhereDefaultsToHome(t *testing.T) {
	project := taskstest.ProjectDir(t)

	mode, err := ResolveMode(project, nil, "")
	require.NoError(t, err)
	assert.Equal(t, paths.ModeHome, mode)
}

// TestHoming_MissingConfigIsSilent proves the default resolution above
// produces NO diagnostic on the clidiag channel either -- not just no error.
// A warning on every invocation of a correctly-configured-by-default tool
// would be the same UX failure in a smaller costume as the fail-loud gate
// this default replaced.
func TestHoming_MissingConfigIsSilent(t *testing.T) {
	project := taskstest.ProjectDir(t)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStderr := os.Stderr
	os.Stderr = w

	mode, resolveErr := ResolveMode(project, nil, "")

	os.Stderr = origStderr
	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)
	require.NoError(t, copyErr)

	require.NoError(t, resolveErr)
	assert.Equal(t, paths.ModeHome, mode)
	assert.Empty(t, buf.String(), "no diagnostic should be written when homing is simply unconfigured")
}

// TestSchema_TopLevelAdditionalPropertiesFalse mirrors
// internal/config/schema_drift_test.go's TestConfigSchema_CoversEveryConfigField
// for taskloom's own, much smaller schema: additionalProperties:false at the
// top level is the mechanism TestConfig_UnknownKeyFailsLoud depends on — a
// schema that lost this would silently accept any key again.
func TestSchema_TopLevelAdditionalPropertiesFalse(t *testing.T) {
	raw, err := resources.GetSchema(SchemaResourceName)
	require.NoError(t, err)
	var doc struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.False(t, doc.AdditionalProperties,
		"taskloom-config-schema's top-level additionalProperties must be false so an unknown key is rejected")

	// Drift gate: every serializable Config field must have a matching schema
	// property, and vice versa -- the schema (not the struct) is the source of
	// truth (see docsgen.go's doc), but the two must stay in exact
	// correspondence or a real field would be silently rejected by
	// additionalProperties:false.
	rt := reflect.TypeFor[Config]()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		_, ok := doc.Properties[name]
		assert.Truef(t, ok, "Config field %q has no matching taskloom-config-schema property", name)
	}
	for name := range doc.Properties {
		found := false
		for i := 0; i < rt.NumField(); i++ {
			tag := rt.Field(i).Tag.Get("yaml")
			fname, _, _ := strings.Cut(tag, ",")
			if fname == name {
				found = true
				break
			}
		}
		assert.Truef(t, found, "taskloom-config-schema declares property %q with no matching Config field", name)
	}
}

// TestHoming_InvalidValueIsRejected proves a set-but-nonsense value (neither
// "home" nor "repo") is a distinct, clearly-worded error -- never silently
// coerced or ignored.
func TestHoming_InvalidValueIsRejected(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "homing: sometimes\n")

	// The schema's enum already rejects this at Load -- confirm ResolveMode
	// surfaces that failure rather than silently falling through.
	_, err := ResolveMode(project, nil, "")
	require.Error(t, err)
}
