package agentkey

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// hermeticGit points git at empty global and system config files so a test
// reads only the repository it built, never the developer's own settings.
func hermeticGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// TestDiscoverer_DocumentedDefaultsApplyWithoutNewDiscoverer pins that the
// per-field "Defaults to ..." promises on Discoverer are true of the TYPE, not
// merely of the NewDiscoverer constructor.
//
// The defect this pins (U135-F06): the defaults existed only inside
// NewDiscoverer, so a Discoverer built as a struct literal — which is how the
// fields' own doc ("Every field is overridable") invites you to use it, and
// how every test in this repo does use it — dereferenced a nil func and
// panicked. Not degraded, not errored: a nil map/func panic out of a signing
// key lookup. The cost is visible in the callers, which all set a throwaway
// ReadFile they never exercise purely to avoid it.
func TestDiscoverer_DocumentedDefaultsApplyWithoutNewDiscoverer(t *testing.T) {
	t.Run("DialAgent defaults to dialing SSH_AUTH_SOCK", func(t *testing.T) {
		t.Setenv("SSH_AUTH_SOCK", "")

		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
		}

		var err error
		require.NotPanics(t, func() { _, err = d.Discover(context.Background(), "") },
			"a Discoverer without DialAgent must fall back to the documented default")
		require.Error(t, err)
		var noKey *NoKeyError
		require.ErrorAs(t, err, &noKey)
		assert.Contains(t, err.Error(), "SSH_AUTH_SOCK",
			"the default must be dialEnvAgent — the error must come from the real one: %q", err.Error())
	})

	t.Run("ReadFile defaults to os.ReadFile", func(t *testing.T) {
		_, line := newTestIdentity(t, "on-disk")
		dir := t.TempDir()
		path := filepath.Join(dir, "id.pub")
		require.NoError(t, os.WriteFile(path, []byte(line), 0o600))

		d := &Discoverer{}

		var pub ssh.PublicKey
		var err error
		require.NotPanics(t, func() { pub, err = d.resolvePublicKey(path) },
			"a Discoverer without ReadFile must fall back to the documented default")
		require.NoError(t, err)
		require.NotNil(t, pub, "the default must actually read the file, not merely be non-nil")
	})

	t.Run("GitConfig defaults to shelling out to git", func(t *testing.T) {
		hermeticGit(t)

		signer, line := newTestIdentity(t, "git-configured")
		repo := t.TempDir()
		runGit(t, repo, "init", "-q")
		pubPath := filepath.Join(repo, "id.pub")
		require.NoError(t, os.WriteFile(pubPath, []byte(line), 0o600))
		runGit(t, repo, "config", "user.signingkey", pubPath)

		ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"git-configured"}}
		d := &Discoverer{
			Dir:       repo,
			DialAgent: func() (agent.Agent, error) { return ag, nil },
		}

		var got *Discovered
		var err error
		require.NotPanics(t, func() { got, err = d.Discover(context.Background(), "") },
			"a Discoverer without GitConfig must fall back to the documented default")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "git config user.signingkey", got.Source,
			"the default must be execGitConfig reading the repository it was pointed at")
		assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
	})
}
