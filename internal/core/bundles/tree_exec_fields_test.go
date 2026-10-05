package bundles

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// requireEveryFieldSet fails when a fixture leaves a field zero, so a field
// added to the bundle type cannot pass the round trips below unexercised.
func requireEveryFieldSet(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	for i := 0; i < rv.NumField(); i++ {
		require.False(t, rv.Field(i).IsZero(), "fixture %T leaves %s unset; set it so the round trip covers it", v, rv.Type().Field(i).Name)
	}
}

// The forward mapping (TreeMCP) and the read (addMCP) are inverses over EVERY
// field of BundleMCP: a field one direction forgets is lost the first time a
// tree is saved or loaded.
func TestTreeMCP_RoundTripsEveryField(t *testing.T) {
	want := BundleMCP{
		Command:      "srv",
		Args:         []string{"--flag"},
		Env:          map[string]string{"A": "1"},
		URL:          "https://mcp.example.com/mcp",
		Headers:      map[string]string{"Authorization": "Bearer ${TOKEN}"},
		ServedBy:     "session-endpoint",
		Tags:         []string{"ctxloom:link_id=pg"},
		Notes:        "notes",
		Installation: "install",
	}
	requireEveryFieldSet(t, want)
	r := &reader{out: &Bundle{}}
	r.addMCP(TreeMCP("remote", want))
	assert.Equal(t, want, r.out.MCP["remote"])
}

// TreeHook and finishHooks are inverses over every field of BundleHook.
func TestTreeHook_RoundTripsEveryField(t *testing.T) {
	order := 7
	want := BundleHook{
		Matcher:         "Bash",
		Command:         "ctxloom",
		Args:            []string{"hook", "session-bind"},
		Type:            "command",
		Prompt:          "prompt",
		Timeout:         5,
		Async:           true,
		PreToolFallback: true,
		Tags:            []string{"ctxloom:link_id=pg"},
		Order:           &order,
	}
	requireEveryFieldSet(t, want)
	r := &reader{out: &Bundle{}, hooks: map[string][]content.Hook{}}
	h := TreeHook(HookEventSessionStart, "guard", want)
	r.hooks[h.Event] = append(r.hooks[h.Event], h)
	r.finishHooks()
	require.Len(t, r.out.Hooks.SessionStart, 1)
	assert.Equal(t, want, r.out.Hooks.SessionStart[0])
}

const remoteTreeDoc = `version: "1.0"
mcp:
  remote:
    url: https://mcp.example.com/mcp
    headers:
      Authorization: Bearer t0ken
    tags: [ctxloom:link_id=remote]
hooks:
  session_start:
    - command: ctxloom
      type: command
      tags: [ctxloom:link_id=remote]
`

// A remote MCP server and a tagged hook survive the whole trip: authored,
// saved to a tree on disk, loaded back by the bundle loader.
func TestLoader_ATreeBundleCarriesRemoteMCPAndTags(t *testing.T) {
	tmpDir := t.TempDir()
	writeTree(t, afero.NewOsFs(), seedBundleRoot(t, tmpDir, paths.LayoutV2), "kit", remoteTreeDoc)
	b, err := NewLoader(NewProjectReader(nil, []string{tmpDir})).Load("kit")
	require.NoError(t, err)

	srv := b.MCP["remote"]
	assert.Equal(t, "https://mcp.example.com/mcp", srv.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer t0ken"}, srv.Headers)
	assert.Equal(t, []string{"ctxloom:link_id=remote"}, srv.Tags)
	require.Len(t, b.Hooks.SessionStart, 1)
	assert.Equal(t, []string{"ctxloom:link_id=remote"}, b.Hooks.SessionStart[0].Tags)
}

// An approval of a tree-authored remote server binds its header as it is on
// disk: rewriting the header in the item file and reloading yields a preimage
// the old approval does not verify over.
func TestTreeMCP_EditingAHeaderOnDiskInvalidatesTheApproval(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)

	fsys := afero.NewOsFs()
	tmpDir := t.TempDir()
	envelope := writeTree(t, fsys, seedBundleRoot(t, tmpDir, paths.LayoutV2), "kit", remoteTreeDoc)
	load := func() []byte {
		b, err := NewLoader(NewProjectReader(nil, []string{tmpDir})).Load("kit")
		require.NoError(t, err)
		srv := b.MCP["remote"]
		payload, err := srv.ContentPayload()
		require.NoError(t, err)
		return payload
	}
	const ref = "kit#mcp/remote"

	approved := load()
	framed := signing.ApproveCountersignPayload(ref, signing.AttestExecMCP, approved)
	armored, err := signing.Sign(framed, signer, signing.NamespaceApprove)
	require.NoError(t, err)
	require.NoError(t, signing.Verify(signing.ApproveCountersignPayload(ref, signing.AttestExecMCP, load()), armored, sshPub, signing.NamespaceApprove),
		"an untouched tree re-derives the approved preimage")

	item := filepath.Join(filepath.Dir(envelope), "mcp", "remote.yaml")
	raw, err := afero.ReadFile(fsys, item)
	require.NoError(t, err)
	require.Contains(t, string(raw), "Bearer t0ken", "the header lives in the item's content file")
	testsupport.WriteFileString(t, fsys, item, strings.ReplaceAll(string(raw), "Bearer t0ken", "Bearer attacker"), 0o644)

	rewritten := load()
	assert.NotEqual(t, approved, rewritten)
	assert.Error(t, signing.Verify(signing.ApproveCountersignPayload(ref, signing.AttestExecMCP, rewritten), armored, sshPub, signing.NamespaceApprove),
		"an approval must not survive a header rewritten in the tree")
}

// The tree item fields are one format generation: a tree written now declares
// it, and an envelope of the generation before it migrates with the marker
// step and nothing else.
func TestExecItemFieldsGeneration_IsWhereTheStepLands(t *testing.T) {
	_, ok := envelopeKind.Steps[execItemFieldsGeneration-1-envelopeKind.Oldest].(execItemFieldsStep)
	assert.True(t, ok)
}

func TestUpgradeEnvelopeAt_MigratesToTheExecItemFieldsGeneration(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := filepath.Join(paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), "kit")
	envelope := filepath.Join(dir, DirectoryFormManifest)
	require.NoError(t, fs.MkdirAll(filepath.Join(dir, "mcp"), 0o755))
	before := execItemFieldsGeneration - 1
	testsupport.WriteFileString(t, fs, envelope, fmt.Sprintf("%s: %d\nversion: 1.0.0\n", schemaver.Key, before), 0o644)
	testsupport.WriteFileString(t, fs, filepath.Join(dir, "mcp", "pg.yaml"), "command: pg\n", 0o644)

	// The previous generation still loads, read exactly as written.
	b, err := NewLoader(NewProjectReader(fs, []string{"/bundles"})).Load("kit")
	require.NoError(t, err)
	assert.Equal(t, BundleMCP{Command: "pg"}, b.MCP["pg"])

	res, err := UpgradeEnvelopeAt(fs, envelope)
	require.NoError(t, err)
	assert.Equal(t, before, res.From)
	assert.Equal(t, execItemFieldsGeneration, res.To)
	assert.Equal(t, []string{execItemFieldsStep{}.Name()}, res.Applied)

	raw, err := afero.ReadFile(fs, envelope)
	require.NoError(t, err)
	assert.Contains(t, string(raw), fmt.Sprintf("%s: %d", schemaver.Key, execItemFieldsGeneration))
}

func TestTreeEnvelope_StampsTheExecItemFieldsGeneration(t *testing.T) {
	raw, err := TreeEnvelope(&Bundle{Version: "1.0.0"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(raw), fmt.Sprintf("%s: %d\n", schemaver.Key, execItemFieldsGeneration)),
		"a tree written now declares the generation whose items may carry url, headers and tags:\n%s", raw)
}
