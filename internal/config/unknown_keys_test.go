package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// loadYAML writes cfgYAML as the project config on a fake fs and loads it the
// way every entry point does (Load → migrate → validate), returning the loaded
// Config so a test can assert on its Warnings.
func loadYAML(t *testing.T, cfgYAML string) *Config {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom/config.yaml", []byte(cfgYAML), 0644))
	cfg, err := Load(WithFS(fs), WithAppDir("/proj/.ctxloom"))
	require.NoError(t, err)
	return cfg
}

// unknownKeyWarnings returns just the unknown-key warnings, so a test can assert
// on them without depending on unrelated load diagnostics.
func unknownKeyWarnings(cfg *Config) []Warning {
	var out []Warning
	for _, w := range cfg.warnings {
		if w.Kind == WarnKindUnknownKey {
			out = append(out, w)
		}
	}
	return out
}

// An unknown key is the silent-no-op trap: ctxloom drops it and launches with a
// context the user never asked for. It must be NAMED, in a message a human can
// act on — not the raw jsonschema pointer soup.
func TestLoad_UnknownTopLevelKey_NamesTheKey(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nprofilez:\n  definitions: {}\n")

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 1, "an unknown top-level key produces exactly one unknown-key warning")
	assert.Contains(t, warns[0].Text, "profilez", "the message must name the offending key")
	assert.Contains(t, warns[0].Text, "config.yaml", "the message must name the file")
	assert.Contains(t, warns[0].Text, "IGNORED", "the message must say the key has no effect")
	assert.Contains(t, warns[0].Text, "profiles", "a near-miss typo must suggest the real key")
}

// A nested typo must name the SECTION too — "use_distiled" alone doesn't tell a
// user where to look.
func TestLoad_UnknownNestedKey_NamesTheFullPath(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nconfig:\n  use_distiled: false\n")

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0].Text, "config.use_distiled", "the message must carry the dotted path")
	assert.Contains(t, warns[0].Text, "use_distilled", "and suggest the near-miss key it meant")
}

// THE trap this whole change exists for: a user copies `profiles.defaults` out of
// a stale doc into a CURRENT-version config. The migrator won't touch it (it is
// version-gated), so today it is silently dropped and the default context is
// empty. The message must name the retired key AND its replacement.
func TestLoad_RetiredProfilesDefaults_AtCurrentVersion_NamesReplacement(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nprofiles:\n  defaults:\n    - dev\n")

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0].Text, "profiles.defaults", "the retired key must be named in full")
	assert.Contains(t, warns[0].Text, "RETIRED", "the user must be told the key is gone, not misspelled")
	assert.Contains(t, warns[0].Text, "default_agent", "and pointed at its replacement")
	assert.Contains(t, warns[0].Text, "agents", "and pointed at its replacement")
}

// Strictness must be applied AFTER migration: an older config whose keys the
// migrator upgrades forward must load CLEAN. Otherwise every user on a
// migratable config eats a fatal finding for a key ctxloom itself would fix.
func TestLoad_OldVersionWithMigratableKey_MigratesWithoutWarning(t *testing.T) {
	cfg := loadYAML(t, "version: 5\nprofiles:\n  defaults:\n    - dev\n  definitions:\n    dev:\n      description: dev\n")

	assert.Empty(t, cfg.warnings, "a migratable old-version config must produce NO warnings: %+v", cfg.warnings)
	require.NotNil(t, cfg.pendingUpgrade, "the load must have upgraded the document in memory")
	assert.Equal(t, "default", cfg.defaultAgent, "the v5→v6 migration rehomes profiles.defaults onto the default agent")
	assert.Equal(t, []string{"dev"}, cfg.agents["default"].Profiles)
}

// The pre-versioning generation (no `version:` at all) runs the whole upgrade
// pipeline; it too must land clean.
func TestLoad_UnversionedWithMigratableKey_MigratesWithoutWarning(t *testing.T) {
	cfg := loadYAML(t, "profiles:\n  defaults:\n    - dev\n")

	assert.Empty(t, cfg.warnings, "an unversioned config must migrate silently: %+v", cfg.warnings)
	assert.Equal(t, []string{"dev"}, cfg.agents["default"].Profiles)
}

// Every unknown key is reported, not just the first: a user who pasted a stale
// block must be told about all of it in one pass, the way the findings gate lists
// every finding rather than the first.
func TestLoad_SeveralUnknownKeys_OneWarningEach(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nconfig:\n  use_distiled: false\n  compaction_chunk: 10\n")

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 2, "one warning per unknown key")
	text := warns[0].Text + "\n" + warns[1].Text
	assert.Contains(t, text, "config.use_distiled")
	assert.Contains(t, text, "config.compaction_chunk")
}

// An unknown key inside a $ref'd section still resolves its known-keys list — the
// suggestion machinery follows the schema pointer, so it must not go blind at a
// $ref boundary.
func TestLoad_UnknownKeyInRefdSection_StillSuggests(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nmcp:\n  servers:\n    s:\n      command: c\n      comand: c\n")

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0].Text, "mcp.servers.s.comand", "the dotted path reaches through the $ref")
	assert.Contains(t, warns[0].Text, "did you mean `command`?")
}

// An unknown key inside an llm.configs.<label> entry sits behind the
// $defs/llmConfig anyOf (one branch per backend: claude-code, codex,
// kiro, ...). The OLD suggestion machinery (configSchemaDocument/
// knownKeysAt) walked the RAW schema JSON expecting a map at every path
// segment; an anyOf node is a JSON array, so the type assertion failed and
// the walk silently returned nil — every backend-specific typo lost its
// did-you-mean and its "known keys at" listing entirely, with no error, just
// a plainer message. Proves the compiled-schema KnownKeys union now reaches
// through anyOf and offers the KIRO branch's own field names (the "big"
// label's type: kiro pins which branch it validates against).
func TestLoad_UnknownKeyInAnyOfBranch_StillSuggests(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nllm:\n  configs:\n    big:\n      type: kiro\n      effrot: high\n")

	// The per-branch fan-out this used to produce (one leaf failure per anyOf
	// alternative, seven identical warnings for one typo) is deduplicated now;
	// see TestLoad_UnknownKeyInAnyOfBranch_ReportedOnceWithoutBranchNoise. What
	// THIS test pins is orthogonal: whatever warnings come out carry a real
	// suggestion drawn from the matched branch, instead of none at all.
	warns := unknownKeyWarnings(cfg)
	require.NotEmpty(t, warns, "warnings: %+v", cfg.warnings)
	for _, w := range warns {
		assert.Contains(t, w.Text, "llm.configs.big.effrot", "the dotted path must reach through the dynamic label and the anyOf branch")
		assert.Contains(t, w.Text, "did you mean `effort`?", "the kiro branch's own field name must be offered, not silently dropped")
		assert.Contains(t, w.Text, "known keys at", "a resolved anyOf branch must list its known keys, not degrade to no suggestion at all")
	}
}

// A schema violation that is NOT an unknown key (a bad enum, a wrong type) keeps
// its old kind and its raw text: this change narrows the unknown-key case out of
// WarnKindValidate, it does not swallow the rest.
func TestLoad_NonUnknownKeySchemaError_StaysValidateKind(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nagents:\n  a:\n    llm: e\n    permissions: nonsense\n")

	assert.Empty(t, unknownKeyWarnings(cfg), "a bad enum is not an unknown key")
	require.Len(t, cfg.warnings, 1)
	assert.Equal(t, WarnKindValidate, cfg.warnings[0].Kind)
	assert.Contains(t, cfg.warnings[0].Text, "config validation warning")
}

// A valid current config must stay silent — a strictness gate that cries wolf on
// good configs is worse than no gate. config.sign.key is deliberately absent from
// this fixture: it is ScopeMachine (a fingerprint/path to this user's own key
// material), so a value at the PROJECT layer — this fixture's own layer — is
// now a genuine layerscope violation, not something "valid" any longer.
func TestLoad_ValidConfig_NoWarnings(t *testing.T) {
	cfg := loadYAML(t, `version: 6
llm:
  configs:
    main:
      type: claude-code
      model: claude-opus-4-8
  defaults:
    primary: main
    fast: main
config:
  use_distilled: true
  sign:
    default: true
default_agent: dev
agents:
  dev:
    llm: main
    profiles: [work]
profiles:
  definitions:
    work:
      description: work
      select_tags: [go]
      commands: ["b#commands/x"]
      fragments:
        - go-style
        - name: testing
          priority: 10
`)

	assert.Empty(t, cfg.warnings, "a valid config must load clean: %+v", cfg.warnings)
}

// TestLoad_UnknownKeyInHomeLayer_StillWarns pins that layering (home <
// project, D2/D3) validates each layer INDEPENDENTLY: a key that is valid at
// the project layer must not mask an unknown key sitting in the home layer —
// before layering, home was never even read alongside a project, so a typo
// there was invisible; now it must fail exactly as loudly as a project-layer
// typo does.
func TestLoad_UnknownKeyInHomeLayer_StillWarns(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()

	homeAppDir := filepath.Join(home, AppDirName)
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(homeAppDir),
		[]byte("version: 6\nprofilez:\n  definitions: {}\n"), 0644))
	require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom/config.yaml",
		[]byte("version: 6\ndefault_agent: dev\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir("/proj/.ctxloom"))
	require.NoError(t, err)

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 1, "an unknown key in the HOME layer must be diagnosed independently of the (valid) project layer")
	assert.Contains(t, warns[0].Text, "profilez", "the message must name the offending key even though it lives in the lower-precedence layer")
}

// TestLoad_UnknownKeyInProjectLayer_NotMaskedByValidHome is the mirror case:
// a valid home layer must not mask an unknown key in the project layer either
// — validity at one layer is never evidence about the other.
func TestLoad_UnknownKeyInProjectLayer_NotMaskedByValidHome(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()

	homeAppDir := filepath.Join(home, AppDirName)
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(homeAppDir),
		[]byte("version: 6\ndefault_agent: dev\n"), 0644))
	require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom/config.yaml",
		[]byte("version: 6\nprofilez:\n  definitions: {}\n"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir("/proj/.ctxloom"))
	require.NoError(t, err)

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0].Text, "profilez")
}

// TestLoad_AgentDriving_NoUnknownKeyWarning pins the fix for a schema-drift
// bug: `driving` exists on agents.Agent and round-trips through YAML, but
// config-schema.json's agent-binding object never gained a matching
// property — so additionalProperties:false rejected it and every load of a
// config setting it emitted a spurious "unknown key ... IGNORED" warning
// even though the value was fully honored. Proves the warning is gone AND
// the field is actually parsed onto the Agent.
func TestLoad_AgentDriving_NoUnknownKeyWarning(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nagents:\n  coord:\n    llm: main\n    profiles: [work]\n    driving: oneshot\n")

	assert.Empty(t, unknownKeyWarnings(cfg), "driving must validate as a known agent-binding key: %+v", unknownKeyWarnings(cfg))
	assert.Empty(t, cfg.warnings, "a config using only documented agent-binding keys must load with no warnings at all: %+v", cfg.warnings)

	a, ok := cfg.Agent("coord")
	require.True(t, ok, "the agent binding must still parse despite the new field")
	assert.Equal(t, agents.DrivingOneshot, a.Driving, "driving: oneshot must be honored, not just accepted")
}

// One typo must produce ONE warning, however many schema leaves it trips.
//
// An llm.configs.<label> entry is validated against an anyOf with one
// alternative per backend, so a single unknown key inside it fails
// additionalProperties in EVERY alternative — seven identical "unknown key
// `llm.configs.big.effrot`" lines for one mistake. Worse, the six alternatives
// whose `type` discriminator did not match each contributed a const failure,
// which raised the "the document is also broken some other way" flag and
// appended a raw jsonschema dump naming /llm/configs/big/type — the one key in
// that block the user got RIGHT. A diagnostic that repeats itself seven times
// and then blames a correct line is worse than the raw error it replaced.
func TestLoad_UnknownKeyInAnyOfBranch_ReportedOnceWithoutBranchNoise(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nllm:\n  configs:\n    big:\n      type: kiro\n      effrot: high\n")

	warns := unknownKeyWarnings(cfg)
	require.Len(t, warns, 1,
		"one typo, one warning: the per-branch fan-out is the schema's business, not the user's")
	assert.Contains(t, warns[0].Text, "llm.configs.big.effrot")

	require.Len(t, cfg.warnings, 1,
		"the rejected branches' const failures are how the schema picked a branch, not a second defect: %+v", cfg.warnings)
	for _, w := range cfg.warnings {
		assert.NotContains(t, w.Text, "/llm/configs/big/type",
			"nothing may blame the `type` the user got right")
	}
}

// The suppression above must not be able to hide a real problem. A config whose
// ONLY fault sits inside a branch — a valid backend with a bad value — has no
// unknown keys at all, so it still reports the raw validation error.
func TestLoad_NonUnknownKeyFaultInsideAnyOfBranch_StillReported(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nllm:\n  configs:\n    big:\n      type: kiro\n      effort: 12\n")

	assert.Empty(t, unknownKeyWarnings(cfg), "a wrong-typed value is not an unknown key")
	require.NotEmpty(t, cfg.warnings, "a fault inside a branch must still be reported")
	assert.Equal(t, WarnKindValidate, cfg.warnings[0].Kind)
}
