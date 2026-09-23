package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestLoadout_YAML_IsAValidLoadout proves ctxloom's own embedded loadout.yaml
// parses as a loadout document whose RUN bundle carries everything ctxloom
// delivers into an engine on its own behalf: its MCP server entry — the
// companion's DYNAMIC declaration, served by the running session's endpoint
// (wire.ServedBySessionEndpoint) with nothing executable on it, so that at
// rest the entry renders nothing and inside a session the engine's dynamic
// approach renders the endpoint — and the always-on isolation-axes fragment.
func TestLoadout_YAML_IsAValidLoadout(t *testing.T) {
	lo, err := bundles.ParseLoadout(loadoutYAML)
	require.NoError(t, err, "ctxloom's loadout.yaml must be a well-formed loadout document")
	assert.True(t, lo.Init.IsZero(), "ctxloom declares no INIT loadout today; a typed field appearing here is a content change to review")
	b := lo.Run

	require.Contains(t, b.MCP, agent.MCPServerName, "loadout must carry ctxloom's own MCP server entry")
	entry := b.MCP[agent.MCPServerName]
	assert.Equal(t, wire.ServedBySessionEndpoint, entry.ServedBy, "ctxloom's entry is served by the running session's endpoint")
	assert.Empty(t, entry.Command, "a dynamic entry names no command: there is no stdio server to launch")
	assert.Empty(t, entry.Args)
	assert.NotContains(t, entry.Notes, "stdio", "the notes describe the entry as it is")

	require.Contains(t, b.Fragments, "isolation-axes", "loadout must carry the always-on isolation guidance")
	assert.Empty(t, b.Fragments["isolation-axes"].Premise, "isolation guidance is unconditional")
}

// TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher is the drift gate:
// ctxloom's loadout is signed uniformly, like every companion's, with the
// release key the embedded trust root already trusts — so an edit to
// loadout.yaml without `just sign-loadouts` fails here, offline. That this
// verification is CIRCULAR (the root vouching for the key ships in the same
// binary as the loadout) is the reader's business, not the signer's: see
// bundles.CompanionLoadout.Self.
func TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "ctxloom's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	var buf bytes.Buffer
	require.NoError(t, loadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), configload.EmbeddedSigners(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Equal(t, "ben+ctxloom@abbitt.me", signer, "ctxloom's loadout must verify as published by the ctxloom release key")
}

// TestLoadout_TamperedLoadoutBodyFailsVerification proves the drift gate
// actually fires: the committed signature against bytes it does not cover
// is withheld, never degraded to "unsigned".
func TestLoadout_TamperedLoadoutBodyFailsVerification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig)

	tampered := append(append([]byte{}, loadoutYAML...), []byte("\n# drift: this byte was never signed\n")...)
	var buf bytes.Buffer
	require.NoError(t, loadout.Emit(&buf, "json", tampered, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), configload.EmbeddedSigners(), time.Now())
	require.Error(t, err)
	assert.Nil(t, decoded)
	assert.Empty(t, signer)
}

// TestCompose_CarriesTheEmbeddedLoadout pins the seam through which the
// embedded bytes reach `ctxloom loadout`: the composition root hands them
// to the CLI, which owns the command (so the documented tree carries it)
// but not the content (go:embed cannot reach outside this package).
func TestCompose_CarriesTheEmbeddedLoadout(t *testing.T) {
	comp := compose(strictness.Sink("ctxloom"))
	assert.Equal(t, loadoutYAML, comp.Loadout.YAML)
	assert.Equal(t, loadoutSig, comp.Loadout.Sig)
}

// TestLoadoutCommand_EmitsTheV2Envelope drives the real command through the
// real root with the real composition — what ctxloom's own self-probe execs
// — and proves the output is a v2 envelope over the embedded document.
func TestLoadoutCommand_EmitsTheV2Envelope(t *testing.T) {
	var out bytes.Buffer
	code := cli.RunWithArgs(compose(strictness.Sink("ctxloom")), []string{loadout.Subcommand, "--" + loadout.FormatFlag, loadout.FormatJSON}, &out)
	require.Equal(t, 0, code, out.String())

	doc, sig, _, err := signing.ParseLoadoutEnvelope(out.Bytes())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, doc)
	assert.Equal(t, loadoutSig, sig)
	assert.Contains(t, out.String(), `"contract": "`+signing.LoadoutContract+`"`)
}
