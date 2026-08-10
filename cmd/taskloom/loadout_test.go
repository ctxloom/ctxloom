package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/companionloadout"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// TestLoadout_YAML_IsAValidBundle proves the embedded loadout.yaml parses as
// a well-formed bundles.Bundle carrying the taskloom fragment, both hooks,
// and the MCP server registration — the content that used to live in
// ctxloom's now-deleted resources/builtin_bundles/taskloom.yaml.
func TestLoadout_YAML_IsAValidBundle(t *testing.T) {
	b, err := bundles.ParseBundle(loadoutYAML)
	require.NoError(t, err, "taskloom's loadout.yaml must be a well-formed bundle")

	require.Contains(t, b.Fragments, "taskloom")
	assert.NotEmpty(t, b.Fragments["taskloom"].Content)

	require.Len(t, b.Hooks.SessionStart, 1)
	assert.Contains(t, b.Hooks.SessionStart[0].Command, "session-bind")
	require.Len(t, b.Hooks.PostFileEdit, 1)
	assert.Contains(t, b.Hooks.PostFileEdit[0].Command, "stamp-plan")

	require.Contains(t, b.MCP, "taskloom")
	assert.Equal(t, "taskloom", b.MCP["taskloom"].Command)
}

// TestLoadout_YAMLFormat_EmitsRawBytesVerbatim proves --format yaml writes
// the exact embedded bytes, unmodified.
func TestLoadout_YAMLFormat_EmitsRawBytesVerbatim(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "yaml", loadoutYAML, loadoutSig))
	assert.Equal(t, loadoutYAML, buf.Bytes())
}

// TestLoadout_JSONFormat_DecodesToIdenticalBundle proves the round trip a
// real companion-discovery probe depends on.
func TestLoadout_JSONFormat_DecodesToIdenticalBundle(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Empty(t, signer, "an unsigned loadout must decode with an empty verified signer, not an error")

	b, err := bundles.ParseBundle(decoded)
	require.NoError(t, err)
	assert.Contains(t, b.Fragments, "taskloom")
}

// TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher is the end-to-end proof
// (S8 loadoutSig seam, filled) that taskloom's loadout is trusted-by-
// construction, not review-pending: the envelope
// `taskloom loadout --format json` actually emits, verified through
// signing.VerifyPublisher against the REAL trust root ctxloom ships
// (config.Config.TrustRoot(), which includes the compiled-in ctxloom release
// key), resolves to that key's principal. It also DOUBLES as the drift gate
// item 2 requires — if loadout.yaml is ever edited without regenerating
// loadout.yaml.sig (`just sign-loadouts`), the committed .sig no longer
// covers the new bytes and this test starts failing loudly, pure-Go and
// offline, no private key required.
func TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "taskloom's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	cfg := &config.Config{}
	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), cfg.TrustRoot(), time.Now())
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
	require.NoError(t, companionloadout.Emit(&buf, "json", tampered, loadoutSig))

	cfg := &config.Config{}
	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), cfg.TrustRoot(), time.Now())
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
// — calling companionloadout.Emit directly, as the tests above do, cannot see
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
	err := companionloadout.Emit(&buf, "toml", loadoutYAML, loadoutSig)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}

// The comment above loadout.yaml's session_start hook set `pre_tool_fallback`
// for antigravity — the harness that shipped with no session-start event and
// needed the bind to land on PreToolUse instead. antigravity was removed in
// 0.7.0 (no currently-registered backend has this gap), but the flag is
// harmless to keep set: it is a no-op for every backend with a working
// session-start event, and keeps the loadout ready for whichever future
// engine needs it next rather than requiring every consumer to re-add it.
//
// This pins taskloom's END of the chain only. The wire crossing is pinned by
// the total-struct parity sweep in internal/lm/grpc/arch_test.go (build tag
// `arch`).
func TestLoadout_SessionBindKeepsPreToolFallback(t *testing.T) {
	b, err := bundles.ParseBundle(loadoutYAML)
	require.NoError(t, err)

	require.Len(t, b.Hooks.SessionStart, 1)
	h := b.Hooks.SessionStart[0]
	require.Contains(t, h.Command, "session-bind")
	assert.True(t, h.PreToolFallback,
		"session-bind should keep pre_tool_fallback set for the next harness that ships with no session-start event")
}
