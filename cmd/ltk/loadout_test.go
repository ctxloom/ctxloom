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
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/shared/companionloadout"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// TestLoadout_YAML_IsAValidBundle proves the embedded loadout.yaml itself
// parses as a well-formed bundles.Bundle and carries the ltk fragment plus
// the task-runner command — the two required contents (S8, item 1/4).
func TestLoadout_YAML_IsAValidBundle(t *testing.T) {
	b, err := bundles.ParseBundle(loadoutYAML)
	require.NoError(t, err, "ltk's loadout.yaml must be a well-formed bundle")

	require.Contains(t, b.Fragments, "ltk", "loadout must carry the ltk fragment")
	assert.NotEmpty(t, b.Fragments["ltk"].Content)

	require.Contains(t, b.Commands, "task-runner", "loadout must carry the task-runner command")
	assert.NotEmpty(t, b.Commands["task-runner"].Content)

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
	assert.Contains(t, b.Commands, "task-runner")
}

// TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher is the end-to-end proof
// (S8 loadoutSig seam, filled) that ltk's loadout is trusted-by-construction,
// not review-pending: the envelope `ltk loadout --format json` actually
// emits, verified through signing.VerifyPublisher against the REAL trust
// root ctxloom ships (config.Config.TrustRoot(), which includes the compiled-
// in ctxloom release key), resolves to that key's principal. It also
// DOUBLES as the drift gate item 2 requires — if loadout.yaml is ever edited
// without regenerating loadout.yaml.sig (`just sign-loadouts`), the
// committed .sig no longer covers the new bytes and this test starts
// failing loudly, pure-Go and offline, no private key required.
func TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "ltk's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	cfg := &config.Config{}
	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), cfg.TrustRoot(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Equal(t, "ben+ctxloom@abbitt.me", signer, "ltk's loadout must verify as published by the ctxloom release key")
}

// TestLoadout_TamperedLoadoutBodyFailsVerification proves the drift gate
// actually fires: the real committed loadoutSig, presented against loadout
// bytes that differ from what it covers (simulating loadout.yaml having
// changed without a re-sign), is withheld — never silently downgraded to
// "unsigned, please review" (spec §10.2).
func TestLoadout_TamperedLoadoutBodyFailsVerification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "ltk's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	tampered := append(append([]byte{}, loadoutYAML...), []byte("\n# drift: this byte was never signed\n")...)
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", tampered, loadoutSig))

	cfg := &config.Config{}
	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), cfg.TrustRoot(), time.Now())
	require.Error(t, err, "a loadout body that drifted from its signature must be withheld, not degraded to unsigned")
	assert.Nil(t, decoded)
	assert.Empty(t, signer)
}

func TestLoadout_UnknownFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := companionloadout.Emit(&buf, "toml", loadoutYAML, loadoutSig)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}

// exampleTaskRunnerRule is the worked example from the task-runner command's
// own instructions (cmd/ltk/loadout.yaml, commands.task-runner.content, step
// 2) — kept here as a literal so this test proves the ACTUAL text the command
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
