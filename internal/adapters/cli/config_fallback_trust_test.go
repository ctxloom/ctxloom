package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The fallback a fault-tolerant command gets when the config cannot load
// holds a real root that trusts no signer, and the warning says the CONFIG
// failed — so a typo in config.yaml does not read as a trust problem.
func TestLoadConfigOrFallback_TrustsNoSignerAndSaysTheConfigFailedToLoad(t *testing.T) {
	var w bytes.Buffer
	cfg := loadConfigOrFallback(func() (*config.Config, error) {
		return nil, errors.New("yaml: line 3: did not find expected key")
	}, &w)

	root := cfg.Trust().Root()
	require.NotNil(t, root, "the fallback's root is a value to ask, never a nil")
	// The embedded release key is trusted to publish by every real root, so a
	// fallback that still read the signer files would trust it.
	embedded := configload.EmbeddedSigners().Entries()
	require.NotEmpty(t, embedded)
	assert.False(t, root.TrustedForNamespace(embedded[0].PublicKey, signing.NamespacePublish, time.Now()).Trusted,
		"the fallback trusts no signer, not even the one compiled in")

	msg := w.String()
	assert.Contains(t, msg, "failed to load config")
	assert.Contains(t, msg, "did not find expected key", "the load error itself is named")
	assert.Contains(t, msg, "no signer is trusted until it loads",
		"the consequence for trust is named, so a denied companion is traced back to the config")
}

// The companion commands do not depend on the project's config: with it
// broken, an allowed companion is still reported allowed, because admission is
// decided from the per-user allow store alone.
func TestCompanionCommands_WithABrokenConfig_StillReadTheAllowStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	resetApp()
	t.Cleanup(resetApp)
	_, binPath := plantRealCompanion(t, bin)
	allowWithYes(t, binPath)
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(root, ".ctxloom", "config.yaml"), "agents: [unclosed\n", 0o644)

	_, err := GetConfig()
	require.Error(t, err, "the fixture must actually break the config, or the fallback is never taken")

	var status bytes.Buffer
	assert.NotPanics(t, func() { printCompanionStatus(&status) })
	assert.NotContains(t, companionLineFor(t, status.String(), bin), "NOT RUN")

	listCmd, listOut := textCmd()
	assert.NotPanics(t, func() { require.NoError(t, runCompanionListCmd(listCmd, nil)) })
	assert.Contains(t, companionLineFor(t, listOut.String(), bin), "allowed")

	showCmd, showOut := formatCmd("json")
	assert.NotPanics(t, func() { require.NoError(t, runCompanionShowCmd(showCmd, []string{binPath})) })
	var shown map[string]any
	require.NoError(t, json.Unmarshal(showOut.Bytes(), &shown), showOut.String())
	assert.Equal(t, true, shown["allowed"])
}
