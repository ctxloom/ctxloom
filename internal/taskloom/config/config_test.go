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
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/priority"
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
	fs.String(homingFlagName, "", "")
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

// TestHomingSchemaDescription_MatchesActualResolveModeBehavior is a
// regression guard: the schema's published `homing` description
// once claimed the opposite of what ResolveMode does — "taskloom refuses to
// guess... must set it before any command... will run" versus the code's
// actual, deliberate silent default to paths.ModeHome when unset (see this
// package's own doc and TestHoming_MissingConfigIsSilent). Since that text
// is rendered into published docs (cmd/taskloom/docs_gen.go), pin that it
// can never again claim taskloom refuses to run unconfigured.
func TestHomingSchemaDescription_MatchesActualResolveModeBehavior(t *testing.T) {
	data, err := resources.GetSchema(SchemaResourceName)
	require.NoError(t, err)

	var parsed struct {
		Properties struct {
			Homing struct {
				Description string `json:"description"`
			} `json:"homing"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))

	desc := parsed.Properties.Homing.Description
	assert.NotContains(t, desc, "refuses to guess", "the schema must not claim taskloom refuses to run unconfigured — it silently defaults to home, see ResolveMode")
	assert.Contains(t, desc, "home", "the description should still name the actual silent default")
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
	got, err := paths.RepoTasksLogPath("/proj")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".taskloom", "tasks.jsonl"), got)
}

// TestHoming_HomeHomedWritesUnderHome pins today's (pre-homing) on-disk
// location unchanged.
func TestHoming_HomeHomedWritesUnderHome(t *testing.T) {
	home := taskstest.Isolate(t)
	got, err := paths.HomeTasksLogPath("swift-amber-falcon")
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
// internal/config/arch_test.go's TestArch_ConfigSchema_CoversEveryConfigField
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

// TestTagSchema_LoadsFromProjectConfig proves tag_schema round-trips through
// the SAME layered Load path homing does -- a project .taskloom/config.yaml
// setting the key wins, and Config.TagSchema carries the declarations
// verbatim (parsing is ParsedTagSchema's job, tested separately below).
func TestTagSchema_LoadsFromProjectConfig(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "tag_schema:\n  - 'tagma.arity:\"triage:type\"=scalar'\n")

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{`tagma.arity:"triage:type"=scalar`}, cfg.TagSchema)
}

// TestTagSchema_AbsentEverywhereDefaultsToBuiltinTriageBaseline mirrors
// TestConfig_AbsentEverywhereDefaultsToHome for tag_schema: no config at any
// layer must still resolve (via ResolvedTagSchema/ParsedTagSchema) to
// DefaultTagSchema's triage:kind/triage:effort/triage:blocks-release scalar
// baseline, so a fresh project gets scalar-collapse with no opt-in required.
func TestTagSchema_AbsentEverywhereDefaultsToBuiltinTriageBaseline(t *testing.T) {
	project := taskstest.ProjectDir(t)

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Empty(t, cfg.TagSchema, "unset at every layer must round-trip as empty, not silently filled in")
	assert.Equal(t, DefaultTagSchema, cfg.ResolvedTagSchema())

	schema, err := cfg.ParsedTagSchema()
	require.NoError(t, err)
	assert.True(t, schema.IsScalar("triage:level"))
	assert.True(t, schema.IsScalar("triage:kind"))
	assert.True(t, schema.IsScalar("triage:effort"))
	assert.True(t, schema.IsScalar("triage:blocks-release"))

	// triage:level is the one hand-assigned priority input, so a value
	// outside the ladder ranks a task as if it were never triaged. The
	// declared range is what `taskloom lint` and the write seam check
	// against; without it a typo'd rung is accepted in silence.
	min, max, ok, err := schema.Range("triage:level")
	require.NoError(t, err)
	require.True(t, ok, "triage:level must declare a range")
	assert.Equal(t, 1.0, min)
	assert.Equal(t, 5.0, max)
}

// TestTagSchema_BuiltinBaselineCompilesAndEvaluatesWithoutError is an
// end-to-end smoke test proving DefaultTagSchema's priority_fn/decay_fn
// declarations actually compile and evaluate cleanly against a real task
// carrying the new-vocabulary tags — the exact concern this feature exists
// for: a schema that parses but silently never evaluates (a syntax error on
// the wrong target, an unreferenced/renamed field) is worse than one that
// fails loud.
func TestTagSchema_BuiltinBaselineCompilesAndEvaluatesWithoutError(t *testing.T) {
	project := taskstest.ProjectDir(t)

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	schema, err := cfg.ParsedTagSchema()
	require.NoError(t, err)

	now := time.Now()
	tagged := func(harpID string, tags ...string) tasks.Task {
		return tasks.Task{HarpID: harpID, Status: tasks.StatusToDo, CreatedAt: now, Tags: tags}
	}
	shared := []string{"triage:kind=defect", "triage:effort=1", "triage:blocks-release=0.7.0"}
	all := []tasks.Task{
		tagged("critical", append([]string{"triage:level=1"}, shared...)...),
		tagged("wishlist", append([]string{"triage:level=5"}, shared...)...),
		tagged("untriaged", shared...),
	}
	results, diag, err := priority.Compute(all, schema, now)
	require.NoError(t, err)
	assert.False(t, diag.NoPriorityFn, "the baseline declares exactly one priority_fn")
	assert.NotZero(t, results["critical"].Raw)

	// The baseline is not merely evaluable, it is DISCRIMINATING: the whole
	// point of the level axis is that a worse consequence outranks a milder
	// one, and that an unrated task sinks below both rather than floating to
	// the top on an absent tag resolving to zero.
	assert.Greater(t, results["critical"].Raw, results["wishlist"].Raw)
	assert.Greater(t, results["wishlist"].Raw, results["untriaged"].Raw)
}

// TestTagSchema_ExplicitConfigOverridesDefaultEntirely proves an explicit
// tag_schema REPLACES the built-in default wholesale (confload's list
// semantics -- see TestKoanf_ListsReplaceNotConcat) rather than merging
// alongside it: a project that declares its own (smaller) schema loses the
// triage baseline's scalar declarations entirely unless it restates them.
func TestTagSchema_ExplicitConfigOverridesDefaultEntirely(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "tag_schema:\n  - 'tagma.arity:\"widget:kind\"=scalar'\n")

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	schema, err := cfg.ParsedTagSchema()
	require.NoError(t, err)
	assert.True(t, schema.IsScalar("widget:kind"))
	assert.False(t, schema.IsScalar("triage:kind"), "an explicit tag_schema must replace the default, not merge with it")
}

// TestTagSchema_MalformedDeclarationFailsLoud proves a tag_schema entry
// tagschema.Parse rejects surfaces as a ParsedTagSchema error naming the
// problem, never a silently empty or partial schema.
func TestTagSchema_MalformedDeclarationFailsLoud(t *testing.T) {
	project := taskstest.ProjectDir(t)
	writeConfig(t, project, "tag_schema:\n  - 'not.a.valid.declaration'\n")

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	_, err = cfg.ParsedTagSchema()
	require.Error(t, err)
}

// TestResolveMode_RejectsUnknownValue pins the one place an unrecognized
// homing value is refused. Everything downstream branches on ModeRepo and
// treats anything else as ModeHome, so a value that slipped through here
// would silently relocate a project's task store rather than error.
func TestResolveMode_RejectsUnknownValue(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, project, "homing: bogus\n")

	_, err := ResolveMode(project, nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `one of "home", "repo"`)

	clean := t.TempDir()
	writeConfig(t, clean, "homing: home\n")
	_, err = ResolveMode(clean, nil, "alsobogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alsobogus")
}

// TestProduct_NilValidatorLeavesKnownPathNil mirrors internal/config's own
// pin: taskloom builds its confload.Product the same way, so it inherits the
// same trap. A method value on a nil *schema.ConfigValidator is a non-nil
// func, which silently converts confload's documented "no schema knowledge"
// branch into an unreachable one.
func TestProduct_NilValidatorLeavesKnownPathNil(t *testing.T) {
	assert.Nil(t, product(nil).KnownPath,
		"no schema means no schema knowledge — confload's nil branch must be reachable")

	validator, err := newValidator()
	require.NoError(t, err, "the real embedded schema must compile, or the other half of this test proves nothing")
	assert.NotNil(t, product(validator).KnownPath,
		"a compiled schema must still be handed through, or the nil case above is vacuous")
}
