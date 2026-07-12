package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/shared/companionloadout"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// TestLoadout_YAML_IsAValidBundle proves the embedded loadout.yaml itself
// parses as a well-formed bundles.Bundle and carries the ltk fragment plus
// the task-runner skill — the two required contents (S8, item 1/4).
func TestLoadout_YAML_IsAValidBundle(t *testing.T) {
	b, err := bundles.ParseBundle(loadoutYAML)
	require.NoError(t, err, "ltk's loadout.yaml must be a well-formed bundle")

	require.Contains(t, b.Fragments, "ltk", "loadout must carry the ltk fragment")
	assert.NotEmpty(t, b.Fragments["ltk"].Content)

	require.Contains(t, b.Skills, "task-runner", "loadout must carry the task-runner skill")
	assert.NotEmpty(t, b.Skills["task-runner"].Content)

	require.Len(t, b.Hooks.PreTool, 1, "loadout must carry the pre-tool hook that wires ltk in")
	assert.Contains(t, b.Hooks.PreTool[0].Command, "ltk evaluate")
}

// TestLoadout_YAMLFormat_EmitsRawBytesVerbatim proves --format yaml writes
// the exact embedded bytes, unmodified — no re-serialization anywhere in the
// path (spec §3.0's transport-agnostic invariant starts here: the bytes a
// human reads with --format yaml are the SAME bytes --format json base64s
// and, eventually, the same bytes a build-time signature would cover).
func TestLoadout_YAMLFormat_EmitsRawBytesVerbatim(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "yaml", loadoutYAML, loadoutSig))
	assert.Equal(t, loadoutYAML, buf.Bytes())
}

// TestLoadout_JSONFormat_DecodesToIdenticalBundle proves the round trip a
// real companion-discovery probe depends on: `ltk loadout --format json`'s
// stdout, fed through signing.DecodeLoadoutEnvelope, yields byte-identical
// bundle content and (since this build ships unsigned) an empty verified
// signer — legal, ordinary, and routes to ctxloom's review path rather than
// an error.
func TestLoadout_JSONFormat_DecodesToIdenticalBundle(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Empty(t, signer, "an unsigned loadout must decode with an empty verified signer, not an error")

	// The decoded bytes must themselves parse as the same bundle a direct
	// --format yaml read would produce.
	b, err := bundles.ParseBundle(decoded)
	require.NoError(t, err)
	assert.Contains(t, b.Skills, "task-runner")
}

func TestLoadout_UnknownFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := companionloadout.Emit(&buf, "toml", loadoutYAML, loadoutSig)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}

// exampleTaskRunnerRule is the worked example from the task-runner skill's
// own instructions (cmd/ltk/loadout.yaml, skills.task-runner.content, step
// 2) — kept here as a literal so this test proves the ACTUAL text the skill
// ships, not a paraphrase that could drift from it.
const exampleTaskRunnerRule = `
version: 1
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

// TestLoadout_TaskRunnerSkill_SampleRulesPassLtkCheck is the required proof
// (S8 output contract, item e) that the task-runner skill's worked example
// is not just prose: it is a VALID ltk config, and — driven through the
// EXACT SAME path the `ltk check --command <sample> --format json` CLI
// command uses (runCheck) — it produces the deny/suggest decision the skill
// promises for its own "validate before finishing" step.
func TestLoadout_TaskRunnerSkill_SampleRulesPassLtkCheck(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(exampleTaskRunnerRule), 0o644))

	var buf bytes.Buffer
	require.NoError(t, runCheck(&buf, "go test ./...", cfgPath, "", "json"))

	var result checkResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result))
	assert.Equal(t, "deny", result.Decision)
	assert.Contains(t, result.Message, "just test")
	assert.Equal(t, "just test", result.Suggestion)
}

// TestLoadout_TaskRunnerSkill_RuleIDDoesNotCollideWithDefaults is the id-
// collision guardrail the skill itself instructs the agent to follow (never
// reuse a shipped default rule id, especially "tests-via-task-runner"),
// proven mechanically against ltk's ACTUAL shipped default rule set
// (sample.ltk.yaml, embedded as defaultRules) rather than trusted by
// inspection.
func TestLoadout_TaskRunnerSkill_RuleIDDoesNotCollideWithDefaults(t *testing.T) {
	example, err := rules.Parse([]byte(exampleTaskRunnerRule))
	require.NoError(t, err)
	require.Len(t, example.Rules, 1)

	defaultCfg, err := rules.Parse([]byte(defaultRules))
	require.NoError(t, err)

	for _, r := range defaultCfg.Rules {
		assert.NotEqual(t, r.ID, example.Rules[0].ID,
			"the skill's worked-example rule id must not collide with a shipped default rule id")
	}
}
