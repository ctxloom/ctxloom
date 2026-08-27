// Tests for cmd/config.go's extracted helpers. The cobra wrappers are
// trivial composition (load config + delegate to helper + write to
// cmd.OutOrStdout), so the testable surface is the section-name switch
// and the YAML marshal step.
package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// fixtureConfig returns a Config populated with a sentinel value in every
// section resolveConfigSection knows about, so each branch's return can
// be distinguished in the marshaled YAML.
func fixtureConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		Settings: config.SettingsConfig{EssenceMaxChars: 8000},
		LM: config.LMConfig{
			Defaults: config.RoleDefaults{Primary: "big"},
			Configs: map[string]config.LLMConfig{
				"big": {Type: "antigravity", Body: map[string]interface{}{"model": "gemini-3-pro"}},
			},
		},
	})
}

func TestResolveConfigSection_KnownSections(t *testing.T) {
	cfg := fixtureConfig()

	cases := []struct {
		name     string
		section  string
		contains string // substring expected in the marshaled YAML
	}{
		{"config", "config", "essence_max_chars"},
		{"llm", "llm", "gemini-3-pro"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveConfigSection(cfg, tc.section)
			require.NoError(t, err)
			require.NotNil(t, got, "known section should return non-nil data")

			var buf bytes.Buffer
			require.NoError(t, renderConfigSection(cfg, tc.section, &buf))
			assert.Contains(t, buf.String(), tc.contains,
				"%s section's marshaled YAML should mention its sentinel value", tc.section)
		})
	}
}

func TestResolveConfigSection_UnknownSection(t *testing.T) {
	cfg := fixtureConfig()
	_, err := resolveConfigSection(cfg, "nope")
	require.Error(t, err)
	// The error message has to list valid sections so the CLI user
	// can recover without reading docs. Every one must appear.
	msg := err.Error()
	for _, want := range []string{"config", "llm"} {
		assert.Contains(t, msg, want, "error must list section %q", want)
	}
	assert.Contains(t, msg, "nope", "error must echo the bad section name")
}

func TestRenderConfigSection_UnknownSurfacesError(t *testing.T) {
	// renderConfigSection must propagate resolveConfigSection's error
	// rather than swallowing it or writing partial output.
	var buf bytes.Buffer
	err := renderConfigSection(fixtureConfig(), "bogus", &buf)
	require.Error(t, err)
	assert.Empty(t, buf.String(), "no output should be written on unknown section")
}

func TestRenderConfigYAML_RoundTripsTopLevelKeys(t *testing.T) {
	// renderConfigYAML serializes the whole config; we verify only that
	// the top-level keys we expect ("llm", "config") are present in the output.
	// Full struct equivalence is yaml.Marshal's problem, not ours.
	var buf bytes.Buffer
	require.NoError(t, renderConfigYAML(fixtureConfig(), &buf))

	out := buf.String()
	for _, key := range []string{"llm:", "config:"} {
		assert.True(t, strings.Contains(out, key),
			"full-config YAML should contain top-level %q (got: %q)", key, out)
	}
}

func TestRenderConfigYAML_OmitsRuntimeOnlyFields(t *testing.T) {
	// Runtime-only Config fields (resolved paths, load warnings, and the
	// in-memory PendingUpgrade) must never appear in `config show`. Before the
	// yaml:"-" tags, a config that upgraded on load dumped PendingUpgrade,
	// whose []byte payload rendered as a raw integer array. Set the pending
	// upgrade explicitly and assert none of the runtime keys leak.
	f := fixtureConfig().ToFixture()
	f.AppRoot = "/tmp/should-not-appear"
	f.Warnings = []config.Warning{{Kind: config.WarnKindValidate, Text: "leaky"}}
	f.PendingUpgrade = &upgrade.Pending{Path: "/x", Data: []byte("version: 6\n")}
	cfg := config.NewFixture(f)

	var buf bytes.Buffer
	require.NoError(t, renderConfigYAML(cfg, &buf))

	out := buf.String()
	for _, leak := range []string{"pendingupgrade", "warnings", "approot", "apppaths", "appdir", "source", "should-not-appear"} {
		assert.NotContains(t, out, leak,
			"config show leaked runtime-only field %q:\n%s", leak, out)
	}
}

// TestConfigFileExists_DistinguishesAbsentFromUnknown pins the core invariant:
// os.Stat has three outcomes, and only two of them are a boolean.
func TestConfigFileExists_DistinguishesAbsentFromUnknown(t *testing.T) {
	dir := t.TempDir()

	t.Run("present", func(t *testing.T) {
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("version: 6\n"), 0o644))
		exists, err := configFileExists(path)
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("genuinely absent", func(t *testing.T) {
		exists, err := configFileExists(filepath.Join(dir, "nope", "config.yaml"))
		require.NoError(t, err, "a missing parent is still just 'absent'")
		assert.False(t, exists)
	})

	t.Run("inconclusive stat is reported, not guessed", func(t *testing.T) {
		// A regular file used as a directory component: ENOTDIR, which is
		// neither "exists" nor fs.ErrNotExist.
		blocker := filepath.Join(dir, "not-a-dir")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
		exists, err := configFileExists(filepath.Join(blocker, "config.yaml"))
		require.Error(t, err, "an unanswerable stat must not be reported as 'absent'")
		assert.False(t, exists)
		assert.Contains(t, err.Error(), "cannot determine whether")
	})
}

// TestRunConfigEdit_InconclusiveStatDoesNotLaunchTheEditor pins the
// reachable half: `os.IsNotExist(err)` alone let every OTHER stat failure fall
// straight through to $EDITOR, so `config edit` opened an editor on a path it
// had just failed to read — and, with the editor exiting 0, reported success.
func TestRunConfigEdit_InconclusiveStatDoesNotLaunchTheEditor(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	// .ctxloom as a FILE makes <cwd>/.ctxloom/config.yaml unstattable (ENOTDIR).
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".ctxloom"), []byte("not a directory"), 0o644))
	// A no-op "editor" that exits 0: if the guard leaks, the command returns nil.
	t.Setenv("VISUAL", "true")
	t.Setenv("EDITOR", "true")

	err := runConfigEdit(&cobra.Command{}, nil)

	require.Error(t, err, "config edit must not launch an editor on a path it could not stat")
	assert.Contains(t, err.Error(), "cannot determine whether")
}

// TestRunConfigCreate_InconclusiveStatIsReportedByTheGuard pins the write
// half: `if err == nil` treated every stat failure as "no config here", so the
// one thing config create promises — never overwriting an existing config.yaml —
// rested on the downstream writer happening to fail too. The guard now reports
// the unanswerable stat itself.
func TestRunConfigCreate_InconclusiveStatIsReportedByTheGuard(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".ctxloom"), []byte("not a directory"), 0o644))

	err := runConfigCreate(&cobra.Command{}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine whether",
		"the refusal must come from the exists-check, not from a later write that happened to fail")
}

// TestRunConfigCreate_WritesTheScaffoldPayload is the characterization test for
// the context.Background() -> cmd.Context() swap. That swap is not
// observable: operations.InitializeProject's signature is
// `InitializeProject(_ context.Context, ...)` — it DISCARDS the context
// entirely (init.go), so no cancellation test can discriminate the two, and
// the row's claim that `config create` cannot be interrupted holds for the
// callee, not the caller. What this pins instead is the payload: config create
// writes a real config.yaml AND remotes.yaml, so the command remains exercised
// end to end through the context it is given.
func TestRunConfigCreate_WritesTheScaffoldPayload(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	require.NoError(t, runConfigCreate(cmd, nil))

	data, err := os.ReadFile(filepath.Join(dir, ".ctxloom", "config.yaml"))
	require.NoError(t, err, "config create must write config.yaml")
	assert.NotEmpty(t, data, "an empty config.yaml is the silent no-op this project keeps shipping")
	remotes, err := os.ReadFile(filepath.Join(dir, ".ctxloom", "remotes.yaml"))
	require.NoError(t, err, "config create also writes remotes.yaml (documented in configCreateLong)")
	assert.NotEmpty(t, remotes)

	// Second run refuses rather than clobbering what it just wrote.
	require.Error(t, runConfigCreate(cmd, nil))
}

// TestConfigShowGet_HonorEveryFormatWithARealPayload pins that a SILENTNOOP
// framing is refuted — the PersistentPostRunE guard (formatWasHonored,
// format.go) already turns an unwired command's `--format json` into a loud
// exit-1 refusal (see format_test.go's TestUnwiredCommand_FormatJSONErrorsLoudly)
// rather than a silent YAML-instead-of-JSON lie — but the underlying gap was real, and
// this is what paying it down has to deliver: EVERY format carries the actual
// configuration, never an empty envelope. Config's fields are unexported and it
// renders via MarshalYAML, so the naive `emit(cmd, cfg, ...)` wiring would emit
// "{}" for json/toml and exit 0: a textbook silent no-op.
func TestConfigShowGet_HonorEveryFormatWithARealPayload(t *testing.T) {
	for _, format := range []string{"text", "json", "yaml", "toml", "markdown"} {
		t.Run(format, func(t *testing.T) {
			cmd, buf := formatCmd(format)
			payload, err := configPayload(fixtureConfig())
			require.NoError(t, err)
			require.NoError(t, emit(cmd, payload, func() error { return renderConfigYAML(fixtureConfig(), cmd.OutOrStdout()) }))

			out := buf.String()
			require.NotEmpty(t, out, "%s rendered zero bytes", format)
			assert.Contains(t, out, "8000", "%s must carry the essence_max_chars sentinel, not an empty envelope", format)
			assert.Contains(t, out, "gemini-3-pro", "%s must carry the llm sentinel", format)
		})
	}
}

// TestConfigPayload_SectionKeysStaySnakeCase pins that `config get`'s payload
// keeps the yaml spelling in every encoding: the section structs carry yaml
// tags only, so a json/toml encoder reading them directly would rename every
// key to its Go field name.
func TestConfigPayload_SectionKeysStaySnakeCase(t *testing.T) {
	section, err := resolveConfigSection(fixtureConfig(), "config")
	require.NoError(t, err)
	payload, err := configPayload(section)
	require.NoError(t, err)

	m, ok := payload.(map[string]any)
	require.True(t, ok, "a section payload must be a plain map, got %T", payload)
	assert.Contains(t, m, "essence_max_chars")
	assert.NotContains(t, m, "EssenceMaxChars")
}

// TestConfigCreateWritesItsSuccessLineToTheCommandWriter pins that
// `config create`'s "Wrote <path>" line went to os.Stdout via a bare
// fmt.Printf, so it was invisible to this package's cobra output-capture tests
// and unredirectable by any embedding frontend.
func TestConfigCreateWritesItsSuccessLineToTheCommandWriter(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	require.NoError(t, runConfigCreate(cmd, nil))

	assert.Contains(t, buf.String(), "Wrote ")
	assert.Contains(t, buf.String(), filepath.Join(dir, ".ctxloom", "config.yaml"))
}
