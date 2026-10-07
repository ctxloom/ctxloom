package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// TestLoadout_YAML_IsAValidLoadout proves the embedded loadout.yaml itself
// parses as a well-formed loadout document whose RUN bundle carries the ltk
// fragment plus the task-runner command.
func TestLoadout_YAML_IsAValidLoadout(t *testing.T) {
	lo, err := bundles.ParseLoadout(loadoutYAML)
	require.NoError(t, err, "ltk's loadout.yaml must be a well-formed loadout document")
	assert.True(t, lo.Init.IsZero(), "ltk declares no INIT loadout today; a typed field appearing here is a content change to review")
	b := lo.Run

	require.Contains(t, b.Fragments, "ltk", "loadout must carry the ltk fragment")
	assert.NotEmpty(t, b.Fragments["ltk"].Content)

	require.Contains(t, b.Commands, "task-runner", "loadout must carry the task-runner command")
	assert.NotEmpty(t, b.Commands["task-runner"].Content)

	require.Len(t, b.Hooks.PreTool, 1, "loadout must carry the pre-tool hook that wires ltk in")
	assert.Contains(t, b.Hooks.PreTool[0].Command, "ltk evaluate")
}

// TestLoadout_YAMLFormat_EmitsRawBytesVerbatim proves --format yaml writes
// the exact embedded bytes, unmodified — no re-serialization anywhere in the
// path: the bytes a human reads are the bytes ctxloom's companion probe
// parses.
func TestLoadout_YAMLFormat_EmitsRawBytesVerbatim(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, loadout.Emit(&buf, loadout.FormatYAML, loadoutYAML))
	assert.Equal(t, loadoutYAML, buf.Bytes())
}

// The two `--format` flags look like a coupling defect at first glance: the
// root registers a PERSISTENT --format over clifmt's five output formats
// (default "text"), and `loadout` registers a LOCAL one over the one loadout
// format (yaml), which shadows it. The two meanings are real. The
// shadowing is not a defect but the mechanism that makes both correct, and
// every remedy that looks obvious breaks something:
//
//   - removing loadout's local flag makes the bare `ltk loadout` inherit the
//     root default "text", which loadout cannot emit — the DEFAULT invocation
//     would start erroring;
//   - renaming it breaks a cross-process wire contract: ctxloom's companion
//     probe execs `<bin> loadout --format yaml`, built from
//     loadout.Subcommand/FormatFlag/FormatYAML, and shared with
//     cmd/taskloom;
//   - unifying the vocabularies would have loadout advertise formats that do
//     not exist.
//
// So this pins the arrangement instead: both flag positions reach the local
// flag, the default is the emittable one, a root-vocabulary value is refused
// LOUDLY rather than silently mis-emitted, and check's five formats are
// untouched by any of it.
func TestRoot_FormatMeansTheLoadoutFormatUnderLoadout(t *testing.T) {
	run := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		root := newRootCmd()
		root.SetArgs(args)
		root.SetOut(&out)
		root.SetErr(&out)
		err := root.Execute()
		return out.String(), err
	}

	t.Run("bare loadout emits the raw bundle, not the root's text default", func(t *testing.T) {
		out, err := run(t, "loadout")
		require.NoError(t, err, "the DEFAULT invocation must work; the root's --format default is not a loadout format")
		assert.Equal(t, string(loadoutYAML), out)
	})

	t.Run("--format yaml after the subcommand emits the document", func(t *testing.T) {
		out, err := run(t, "loadout", "--format", "yaml")
		require.NoError(t, err, "this is the exact argv ctxloom's companion probe execs")
		assert.Equal(t, string(loadoutYAML), out)
	})

	t.Run("--format yaml in the persistent position reaches the same flag", func(t *testing.T) {
		out, err := run(t, "--format", "yaml", "loadout")
		require.NoError(t, err)
		assert.Equal(t, string(loadoutYAML), out, "the shadow must resolve toward the local flag")
	})

	t.Run("a root-vocabulary format is refused loudly", func(t *testing.T) {
		for _, f := range []string{"text", "json", "toml", "markdown"} {
			out, err := run(t, "loadout", "--format", f)
			require.Error(t, err, "loadout must not silently accept %q", f)
			assert.NotContains(t, out, "fragments:", "nothing may be emitted for an unsupported loadout format")
		}
	})

	t.Run("check still speaks the root's five formats", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("schema_version: 1\nrules: []\n"), 0o644))
		for _, f := range []string{"text", "json", "yaml", "toml", "markdown"} {
			out, err := run(t, "check", "--command", "git status", "--config", cfgPath, "--format", f)
			require.NoError(t, err, "check --format %s", f)
			assert.Contains(t, out, "allow")
		}
	})
}

func TestLoadout_UnknownFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := loadout.Emit(&buf, "toml", loadoutYAML)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}

// exampleTaskRunnerRule is the worked example from the task-runner command's
// own instructions (cmd/ltk/loadout.yaml, commands.task-runner.content, step
// 2) — kept here as a literal so this test proves the ACTUAL text the command
// ships, not a paraphrase that could drift from it.
const exampleTaskRunnerRule = `
schema_version: 1
defaults:
  on_parse_error: allow
  repeat_window_seconds: 30
rules:
  - id: go-test-via-just
    match: { command: [go, test] }
    mode: confirm
    message: "Run tests through just test, not go test directly, so the suite matches CI."
    suggest: "just test"
`

// TestLoadout_TaskRunnerCommand_SampleRulesPassLtkCheck is the required proof
// (S8 output contract, item e) that the task-runner command's worked example
// is not just prose: it is a VALID ltk config, and — driven through the
// EXACT SAME path the `ltk check --command <sample> --format json` CLI
// command uses (runCheck) — it produces the deny/suggest decision the command
// promises for its own "validate before finishing" step.
func TestLoadout_TaskRunnerCommand_SampleRulesPassLtkCheck(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(exampleTaskRunnerRule), 0o644))

	var buf, diag bytes.Buffer
	require.NoError(t, runCheck(&buf, &diag, "go test ./...", cfgPath, "", "json"))

	var result checkResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result))
	assert.Equal(t, "deny", result.Decision)
	assert.Contains(t, result.Message, "just test")
	assert.Equal(t, "just test", result.Suggestion)
}

// TestLoadout_TaskRunnerCommand_RuleIDDoesNotCollideWithDefaults is the id-
// collision guardrail the command itself instructs the agent to follow (never
// reuse a shipped default rule id, especially "tests-via-task-runner"),
// proven mechanically against ltk's ACTUAL shipped default rule set
// (sample.ltk.yaml, embedded as defaultRules) rather than trusted by
// inspection.
func TestLoadout_TaskRunnerCommand_RuleIDDoesNotCollideWithDefaults(t *testing.T) {
	example, err := rules.Parse([]byte(exampleTaskRunnerRule))
	require.NoError(t, err)
	require.Len(t, example.Rules, 1)

	defaultCfg, err := rules.Parse([]byte(defaultRules))
	require.NoError(t, err)

	for _, r := range defaultCfg.Rules {
		assert.NotEqual(t, r.ID, example.Rules[0].ID,
			"the command's worked-example rule id must not collide with a shipped default rule id")
	}
}
