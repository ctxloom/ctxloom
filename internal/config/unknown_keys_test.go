package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	for _, w := range cfg.Warnings {
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

	assert.Empty(t, cfg.Warnings, "a migratable old-version config must produce NO warnings: %+v", cfg.Warnings)
	require.NotNil(t, cfg.PendingUpgrade, "the load must have upgraded the document in memory")
	assert.Equal(t, "default", cfg.DefaultAgent, "the v5→v6 migration rehomes profiles.defaults onto the default agent")
	assert.Equal(t, []string{"dev"}, cfg.Agents["default"].Profiles)
}

// The pre-versioning generation (no `version:` at all) runs the whole upgrade
// pipeline; it too must land clean.
func TestLoad_UnversionedWithMigratableKey_MigratesWithoutWarning(t *testing.T) {
	cfg := loadYAML(t, "profiles:\n  defaults:\n    - dev\n")

	assert.Empty(t, cfg.Warnings, "an unversioned config must migrate silently: %+v", cfg.Warnings)
	assert.Equal(t, []string{"dev"}, cfg.Agents["default"].Profiles)
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

// A schema violation that is NOT an unknown key (a bad enum, a wrong type) keeps
// its old kind and its raw text: this change narrows the unknown-key case out of
// WarnKindValidate, it does not swallow the rest.
func TestLoad_NonUnknownKeySchemaError_StaysValidateKind(t *testing.T) {
	cfg := loadYAML(t, "version: 6\nagents:\n  a:\n    engine: e\n    permissions: nonsense\n")

	assert.Empty(t, unknownKeyWarnings(cfg), "a bad enum is not an unknown key")
	require.Len(t, cfg.Warnings, 1)
	assert.Equal(t, WarnKindValidate, cfg.Warnings[0].Kind)
	assert.Contains(t, cfg.Warnings[0].Text, "config validation warning")
}

// A valid current config must stay silent — a strictness gate that cries wolf on
// good configs is worse than no gate.
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
    key: SHA256:abc
default_agent: dev
agents:
  dev:
    engine: main
    profiles: [work]
profiles:
  definitions:
    work:
      description: work
      select_tags: [go]
      prompts: ["b#prompts/x"]
      fragments:
        - go-style
        - name: testing
          priority: 10
`)

	assert.Empty(t, cfg.Warnings, "a valid config must load clean: %+v", cfg.Warnings)
}
