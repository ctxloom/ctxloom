package agentkey

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// TestGitSigningKey_RepoLocalConfigNamesTheFileRead is the MEASUREMENT behind
// the trust boundary's premise, kept as a pin because everything else here
// depends on it being true: `git config --get` runs with cmd.Dir set to the working
// repository, so a value written into that repository's OWN .git/config — the
// file that arrives with a clone — decides which path ctxloom opens.
//
// This is not a defect on its own: honouring git's configuration IS step 2 of
// the documented chain, and git itself signs commits the same way. It is
// pinned so that the bound below has a stated reason, and so that anyone
// revisiting the trust boundary starts from a fact rather than an assumption.
func TestGitSigningKey_RepoLocalConfigNamesTheFileRead(t *testing.T) {
	hermeticGit(t)

	signer, line := newTestIdentity(t, "repo-local")
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")

	// Written with plain `git config`, i.e. into <repo>/.git/config — nothing
	// global, nothing the user of the repository ever typed.
	pubPath := filepath.Join(repo, "planted.pub")
	require.NoError(t, os.WriteFile(pubPath, []byte(line), 0o600))
	runGit(t, repo, "config", "user.signingkey", pubPath)

	raw, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "planted.pub",
		"fixture must have planted the value in the REPOSITORY's config, not anywhere else")

	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"repo-local"}}
	d := &Discoverer{Dir: repo, DialAgent: func() (agent.Agent, error) { return ag, nil }}

	got, err := d.Discover(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "git config user.signingkey", got.Source)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint,
		"the repository's own config selected the identity")
}

// TestResolvePublicKey_OversizedFileIsRefused pins the ceiling on what a
// key-value path may read.
//
// The defect this pins: the read was os.ReadFile with no bound, on
// a path the repository being worked in can name. A file large enough to
// matter was slurped whole before anything looked at it.
func TestResolvePublicKey_OversizedFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.pub")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("A", maxPublicKeyBytes+4096)), 0o600))

	d := &Discoverer{}

	_, err := d.resolvePublicKey(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large to be a public key file",
		"an oversized file must be refused by SIZE, not read whole and then rejected as unparseable: %q", err.Error())
}

// TestResolvePublicKey_EndlessFileTerminates is the case the bound exists for:
// /dev/zero reports size 0 and never reaches EOF, so an unbounded os.ReadFile
// grows its buffer until the process dies. A repository's own .git/config can
// name it (see TestGitSigningKey_RepoLocalConfigNamesTheFileRead).
//
// This assertion is deliberately NOT driven red: against the unbounded code it
// does not fail, it consumes the machine's memory until something is killed,
// and sibling work shares this box. The oversized-regular-file pin above is
// the drivable one; this pins that the same ceiling makes the endless case
// terminate at all.
func TestResolvePublicKey_EndlessFileTerminates(t *testing.T) {
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skipf("no /dev/zero on this platform: %v", err)
	}

	d := &Discoverer{}
	done := make(chan error, 1)
	go func() {
		_, err := d.resolvePublicKey("/dev/zero")
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "/dev/zero is not a public key")
		assert.Contains(t, err.Error(), "too large to be a public key file")
	case <-time.After(30 * time.Second):
		t.Fatal("resolvePublicKey did not terminate on an endless file — the read is unbounded")
	}
}
