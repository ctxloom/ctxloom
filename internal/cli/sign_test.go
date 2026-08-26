package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// discovererWithSoleAgentIdentity returns an agentkey.Discoverer wired to a
// real in-memory ssh-agent keyring (agent.NewKeyring — no socket, no real
// git binary) holding exactly one identity, and no git config value — the
// "ssh-agent (sole identity)" step of the chain (spec §7A.4 step 2).
func discovererWithSoleAgentIdentity(t *testing.T) (*agentkey.Discoverer, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	kr := agent.NewKeyring()
	require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: priv}))

	signers, err := kr.Signers()
	require.NoError(t, err)
	require.Len(t, signers, 1)

	return &agentkey.Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return kr, nil },
		ReadFile:  func(path string) ([]byte, error) { return nil, assert.AnError },
	}, signers[0]
}

func TestRunSign_WritesVerifiableSigForBareLocalBundle(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	discoverer, signer := discovererWithSoleAgentIdentity(t)

	cmd, out := testCmd()
	require.NoError(t, runSign(cmd, cfg, discoverer, "my-tools", false, ""))
	assert.Contains(t, out.String(), "my-tools")
	assert.Contains(t, out.String(), ".sig")

	bundlePath := cfg.GetBundleDirs()[0] + "/my-tools.yaml"
	bundleBytes, err := afero.ReadFile(afero.NewOsFs(), bundlePath)
	require.NoError(t, err)
	sigBytes, err := afero.ReadFile(afero.NewOsFs(), bundlePath+".sig")
	require.NoError(t, err)

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
	principal, verr := signing.VerifyPublisher(bundleBytes, sigBytes, root, time.Now())
	require.NoError(t, verr)
	assert.Equal(t, "me@example.com", principal)
}

// TestRunSign_KeyFlagMatchesAgentKeyByCommentName exercises the --key name
// form end to end through runSign: a ctxloom-specific value (a
// ssh-agent comment) resolves to the right identity even though the agent
// holds a second, unrelated key.
func TestRunSign_KeyFlagMatchesAgentKeyByCommentName(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	_, wantedPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	kr := agent.NewKeyring()
	require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: otherPriv, Comment: "other@example.com"}))
	require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: wantedPriv, Comment: "ben@abbitt.me"}))

	discoverer := &agentkey.Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return kr, nil },
		ReadFile:  func(path string) ([]byte, error) { return nil, assert.AnError },
	}

	cmd, out := testCmd()
	require.NoError(t, runSign(cmd, cfg, discoverer, "my-tools", false, "ben@abbitt"))

	wantedSigner, err := ssh.NewSignerFromSigner(wantedPriv)
	require.NoError(t, err)
	assert.Contains(t, out.String(), ssh.FingerprintSHA256(wantedSigner.PublicKey()))
}

func TestRunSign_ItemRefReportsContainingBundle(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name:      "my-tools",
		Fragments: map[string]operations.BundleFragmentInput{"go-testing": {Content: "x", NoDistill: true}},
	})
	require.NoError(t, err)

	discoverer, _ := discovererWithSoleAgentIdentity(t)
	cmd, out := testCmd()
	require.NoError(t, runSign(cmd, cfg, discoverer, "my-tools#fragments/go-testing", false, ""))
	assert.Contains(t, out.String(), "Signing bundle my-tools (contains fragments/go-testing)")
}

func TestRunSign_NoKeyAnywhereIsHardError(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	discoverer := &agentkey.Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return nil, assert.AnError },
		ReadFile:  func(path string) ([]byte, error) { return nil, assert.AnError },
	}

	cmd, _ := testCmd()
	err = runSign(cmd, cfg, discoverer, "my-tools", false, "")
	require.Error(t, err)
	var noKeyErr *agentkey.NoKeyError
	require.ErrorAs(t, err, &noKeyErr)

	// And nothing was written: failing to sign must never leave a silent
	// unsigned publish artifact behind.
	bundlePath := cfg.GetBundleDirs()[0] + "/my-tools.yaml"
	_, statErr := afero.NewOsFs().Stat(bundlePath + ".sig")
	assert.Error(t, statErr, ".sig must not exist when key discovery failed")
}

func TestRunSign_AllSignsEveryLocalBundle(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "alpha"})
	require.NoError(t, err)
	_, err = operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "beta"})
	require.NoError(t, err)

	discoverer, _ := discovererWithSoleAgentIdentity(t)
	cmd, _ := testCmd()
	require.NoError(t, runSign(cmd, cfg, discoverer, "", true, ""))

	for _, name := range []string{"alpha", "beta"} {
		p := cfg.GetBundleDirs()[0] + "/" + name + ".yaml.sig"
		_, err := afero.NewOsFs().Stat(p)
		assert.NoError(t, err, "%s should have been signed", name)
	}
}

// TestRunSign_FormatJSON_EmitsStructuredTargets pins `ctxloom sign` as a
// former emit() straggler now routed through it: --format json returns the
// per-target sign result instead of the human "signed by X (Y)" text lines.
func TestRunSign_FormatJSON_EmitsStructuredTargets(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	discoverer, signer := discovererWithSoleAgentIdentity(t)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("format", "text", "")
	require.NoError(t, cmd.Flags().Set("format", "json"))
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, runSign(cmd, cfg, discoverer, "my-tools", false, ""))

	var result signCmdResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Len(t, result.Signed, 1)
	assert.Equal(t, "my-tools", result.Signed[0].Bundle)
	assert.Contains(t, result.Signed[0].SigPath, ".sig")
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), result.Signed[0].Fingerprint)
}

func TestRunSign_RefAndAllTogetherIsUsageError(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	cmd, _ := testCmd()
	err := runSign(cmd, cfg, discoverer, "my-tools", true, "")
	require.Error(t, err)
}

func TestRunSign_NeitherRefNorAllIsUsageError(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	cmd, _ := testCmd()
	err := runSign(cmd, cfg, discoverer, "", false, "")
	require.Error(t, err)
}

// setupSignTestDir mirrors internal/operations' setupBundleTestDir (real
// tempdir; bundle Save writes real files, not afero memmap).
func setupSignTestDir(t *testing.T) (string, *config.Config) {
	t.Helper()
	appDir := t.TempDir() + "/.ctxloom"
	require.NoError(t, afero.NewOsFs().MkdirAll(paths.LocalBundlesPath(appDir), 0o755))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	return appDir, cfg
}

// `ctxloom bundle sign --all` over a project whose bundle dirs used to resolve to
// nothing printed "no local bundles to sign" and exited 0 — signing nothing
// looked exactly like having nothing to sign. That is also the visible face
// of the known GetBundleDirs-points-at-cache/bundles defect: the command
// that is supposed to sign a publishing repo's content quietly signs none of
// it.
func TestRunSign_AllWithNoBundlesIsAnError(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	cmd, _ := testCmd()

	err := runSign(cmd, cfg, discoverer, "", true, "")
	require.Error(t, err, "--all that signed nothing must not exit 0")
	assert.Contains(t, err.Error(), "sign", "the error names what was searched")
}

// TestRunSign_JSONSignedIsAlwaysAnArray pins the shape: the
// zero-target path used to emit signCmdResult{} — a nil slice, rendering
// "signed": null — while the normal path emitted an initialised slice, forcing
// a JSON consumer to handle both. It no longer can: the only construction site
// initialises the slice, and the zero-target arm returns an ERROR rather than
// emitting anything at all. This goes red if either property is
// undone — if the slice is left nil, or if the empty case is ever made to emit
// a result again.
func TestRunSign_JSONSignedIsAlwaysAnArray(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)

	jsonCmd := func() (*cobra.Command, *bytes.Buffer) {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.Flags().String("format", "text", "")
		require.NoError(t, cmd.Flags().Set("format", "json"))
		var out bytes.Buffer
		cmd.SetOut(&out)
		return cmd, &out
	}

	t.Run("a signed run renders signed as a JSON array", func(t *testing.T) {
		_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "my-tools"})
		require.NoError(t, err)

		cmd, out := jsonCmd()
		require.NoError(t, runSign(cmd, cfg, discoverer, "my-tools", false, ""))

		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(out.Bytes(), &raw))
		require.Contains(t, raw, "signed")
		assert.NotEqual(t, "null", string(raw["signed"]), `"signed" must never be JSON null`)
		assert.Equal(t, byte('['), raw["signed"][0], `"signed" is an array in every emitted result`)
	})

	t.Run("the zero-target path emits nothing at all", func(t *testing.T) {
		_, emptyCfg := setupSignTestDir(t)
		cmd, out := jsonCmd()
		require.Error(t, runSign(cmd, emptyCfg, discoverer, "", true, ""),
			"signing nothing is a failed run, so there is no second result shape for a consumer to handle")
		assert.Empty(t, out.String(), "a failed run emits no result document")
	})
}
