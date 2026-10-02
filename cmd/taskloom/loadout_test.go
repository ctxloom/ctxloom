package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// TestLoadout_YAML_IsAValidLoadout proves the embedded loadout.yaml parses
// as a well-formed loadout document whose RUN bundle carries the taskloom
// fragment, both hooks, and the MCP server registration.
func TestLoadout_YAML_IsAValidLoadout(t *testing.T) {
	lo, err := bundles.ParseLoadout(loadoutYAML)
	require.NoError(t, err, "taskloom's loadout.yaml must be a well-formed loadout document")
	assert.True(t, lo.Init.IsZero(), "taskloom declares no INIT loadout today; a typed field appearing here is a content change to review")
	b := lo.Run

	require.Contains(t, b.Fragments, "taskloom")
	assert.NotEmpty(t, b.Fragments["taskloom"].Content)

	require.Len(t, b.Hooks.SessionStart, 1)
	assert.Equal(t, "ctxloom", b.Hooks.SessionStart[0].Command, "exec form: the executable alone")
	assert.Equal(t, []string{"hook", "session-bind"}, b.Hooks.SessionStart[0].Args)
	require.Len(t, b.Hooks.PostFileEdit, 1)
	assert.Equal(t, []string{"hook", "stamp-plan"}, b.Hooks.PostFileEdit[0].Args)

	require.Contains(t, b.MCP, "taskloom")
	assert.Equal(t, "taskloom", b.MCP["taskloom"].Command)
}

// TestLoadout_YAMLFormat_EmitsRawBytesVerbatim proves --format yaml writes
// the exact embedded bytes, unmodified.
func TestLoadout_YAMLFormat_EmitsRawBytesVerbatim(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, loadout.Emit(&buf, "yaml", loadoutYAML, loadoutSig))
	assert.Equal(t, loadoutYAML, buf.Bytes())
}

// TestLoadout_JSONFormat_DecodesToIdenticalDocument proves the round trip a
// real companion-discovery probe depends on.
func TestLoadout_JSONFormat_DecodesToIdenticalDocument(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, loadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Empty(t, signer, "an unsigned loadout must decode with an empty verified signer, not an error")

	lo, err := bundles.ParseLoadout(decoded)
	require.NoError(t, err)
	b := lo.Run
	assert.Contains(t, b.Fragments, "taskloom")
}

// TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher is the end-to-end proof
// (S8 loadoutSig seam, filled) that taskloom's loadout is trusted-by-
// construction, not review-pending: the envelope
// `taskloom loadout --format json` actually emits, verified through
// signing.VerifyPublisher against the REAL trust root ctxloom ships
// (configload.EmbeddedSigners(), the compiled-in ctxloom release
// key), resolves to that key's principal. It also DOUBLES as the drift gate
// item 2 requires — if loadout.yaml is ever edited without regenerating
// loadout.yaml.sig (`just sign-loadouts`), the committed .sig no longer
// covers the new bytes and this test starts failing loudly, pure-Go and
// offline, no private key required.
func TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "taskloom's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	var buf bytes.Buffer
	require.NoError(t, loadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), configload.EmbeddedSigners(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Equal(t, "ben+ctxloom@abbitt.me", signer, "taskloom's loadout must verify as published by the ctxloom release key")
}

// TestLoadout_TamperedLoadoutBodyFailsVerification proves the drift gate
// actually fires: the real committed loadoutSig, presented against loadout
// bytes that differ from what it covers (simulating loadout.yaml having
// changed without a re-sign), is withheld — never silently downgraded to
// "unsigned, please review" (spec §10.2).
func TestLoadout_TamperedLoadoutBodyFailsVerification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "taskloom's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	tampered := append(append([]byte{}, loadoutYAML...), []byte("\n# drift: this byte was never signed\n")...)
	var buf bytes.Buffer
	require.NoError(t, loadout.Emit(&buf, "json", tampered, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), configload.EmbeddedSigners(), time.Now())
	require.Error(t, err, "a loadout body that drifted from its signature must be withheld, not degraded to unsigned")
	assert.Nil(t, decoded)
	assert.Empty(t, signer)
}

// `loadout` is the one command on this tree that declares its own local
// --format, with a narrower vocabulary (yaml/json) and a different default
// (yaml, not text) than the root's persistent one. Two consequences follow
// and they are NOT the same, which is why both are pinned here.
//
// A root-vocabulary value the local flag does not know is refused loudly —
// `--format markdown` errors and writes nothing, which is the acceptable
// half.
//
// The --json shorthand is the other half and it was silent. --json is
// documented as nothing but shorthand for --format json and is declared
// persistently on the root precisely so every command can be relied on to
// honor it; loadout read only its own local variable, so `loadout --json`
// accepted the flag, exited 0, and emitted YAML. A caller asking for JSON
// and being handed YAML with no diagnostic is the failure this repo names
// silent-no-op.
func TestLoadout_JSONShorthandIsHonored(t *testing.T) {
	out := runLoadout(t, "--json")

	assert.NotEqual(t, string(loadoutYAML), out,
		"--json must not fall through to the raw YAML body")
	decoded, _, err := signing.DecodeLoadoutEnvelope([]byte(out), nil, time.Now())
	require.NoError(t, err, "--json must emit the same envelope --format json does")
	assert.Equal(t, loadoutYAML, decoded)
}

// An explicit --format still wins over the shorthand, and the local
// vocabulary still refuses what it cannot emit rather than degrading.
func TestLoadout_ExplicitFormatBeatsShorthandAndRefusesUnknown(t *testing.T) {
	assert.Equal(t, string(loadoutYAML), runLoadout(t, "--json", "--format", "yaml"),
		"an explicit --format is the more specific request and must win")

	loadout := newLoadoutCmd()
	rootCmd.AddCommand(loadout)
	t.Cleanup(func() { rootCmd.RemoveCommand(loadout) })
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"loadout", "--format", "markdown"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	require.Error(t, err, "a root-vocabulary format loadout cannot emit must be refused, not degraded")
	assert.NotContains(t, err.Error(), "taskloom:", "the refusal names the format, not the store")
}

// runLoadout drives `loadout` through the REAL root command, which is the
// only place the root's persistent flags and the subcommand's local ones meet
// — calling loadout.Emit directly, as the tests above do, cannot see
// this interaction at all.
//
// rootCmd is package-global and shared with every other test in this
// package (main_test.go's executeFailingUnderFormat drives the same
// instance), so this resets the persistent --format/--json flags both
// inbound and outbound via resetGlobalFormatFlags — the callers below pass
// --json, and without the outbound reset its Changed bit survives this test
// and leaks into whichever test runs next, in this file or any other.
func runLoadout(t *testing.T, args ...string) string {
	t.Helper()
	loadout := newLoadoutCmd()
	rootCmd.AddCommand(loadout)
	t.Cleanup(func() { rootCmd.RemoveCommand(loadout) })

	resetGlobalFormatFlags()
	t.Cleanup(resetGlobalFormatFlags)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"loadout"}, args...))
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	require.NoError(t, rootCmd.Execute())
	return buf.String()
}

func TestLoadout_UnknownFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := loadout.Emit(&buf, "toml", loadoutYAML, loadoutSig)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}

// loadout.yaml's session_start hook sets `pre_tool_fallback` so the bind can
// land on PreToolUse for an agent without a session-start event. It is a no-op
// for every backend with a working session-start event.
func TestLoadout_SessionBindKeepsPreToolFallback(t *testing.T) {
	lo, err := bundles.ParseLoadout(loadoutYAML)
	require.NoError(t, err)
	b := lo.Run

	require.Len(t, b.Hooks.SessionStart, 1)
	h := b.Hooks.SessionStart[0]
	require.Equal(t, []string{"hook", "session-bind"}, h.Args)
	assert.True(t, h.PreToolFallback,
		"session-bind should keep pre_tool_fallback set for the next harness that ships with no session-start event")
}
