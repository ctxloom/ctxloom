package agentkey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// privateKeyPaste is what a user hands --key when they paste the wrong half of
// their keypair — the mistake this package's whole "ctxloom never reads private
// key material" posture exists to make safe. The needle is a marker: if it
// reaches an error string, the real bytes would have too.
const privateKeyPaste = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAA-NEEDLE-SECRET-MATERIAL\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

const privateKeyNeedle = "NEEDLE-SECRET-MATERIAL"

// TestExplicitKey_PrivateKeyMaterialIsNeverEchoed pins that a --key/sign.key
// value carrying private key material is never reproduced in an error.
//
// The defect this pins (U135-F07): every failure arm interpolated the raw
// value. `--key %q: not a recognized fingerprint or public key`, the
// NoKeyError "Looked for: --key/sign.key <value>" line, "no ssh-agent identity
// comment matches %q", and — worst — the file arm, which handed the value to
// os.ReadFile as a PATH and wrapped the resulting *fs.PathError, so the whole
// private key came back inside the message. That the value is also in argv and
// shell history bounds the blast radius; it does not license the tool to print
// it into a message users paste into bug reports and CI logs.
//
// The counter-assertions matter as much: a value that is NOT key material must
// still be echoed, because naming it back is the entire value of the error.
func TestExplicitKey_PrivateKeyMaterialIsNeverEchoed(t *testing.T) {
	signer, _ := newTestIdentity(t, "someone@example.com")

	assertRedacted := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.NotContains(t, err.Error(), privateKeyNeedle,
			"private key bytes must never reach an error string: %q", err.Error())
		assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY",
			"not even the header: %q", err.Error())
		assert.Contains(t, strings.ToLower(err.Error()), "redacted",
			"the user must be told the value was withheld, not silently dropped: %q", err.Error())
	}

	t.Run("no key matches: the value is redacted in the wrap", func(t *testing.T) {
		ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"someone@example.com"}}
		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
			DialAgent: func() (agent.Agent, error) { return ag, nil },
		}

		_, err := d.Discover(context.Background(), privateKeyPaste)
		assertRedacted(t, err)
	})

	t.Run("agent unreachable: the value is redacted in NoKeyError.Looked", func(t *testing.T) {
		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
			DialAgent: func() (agent.Agent, error) { return nil, errors.New("SSH_AUTH_SOCK is not set") },
		}

		_, err := d.Discover(context.Background(), privateKeyPaste)
		assertRedacted(t, err)
		var noKey *NoKeyError
		require.ErrorAs(t, err, &noKey)
	})

	t.Run("ambiguous name: the value is redacted in AmbiguousKeyNameError", func(t *testing.T) {
		e := &AmbiguousKeyNameError{
			Name:       privateKeyPaste,
			Candidates: []Candidate{{Fingerprint: "SHA256:aaa", Type: "ssh-ed25519", Comment: "one"}},
		}
		assertRedacted(t, e)
	})

	t.Run("git config value: the value is redacted too", func(t *testing.T) {
		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) {
				return privateKeyPaste, true, nil
			},
			DialAgent: func() (agent.Agent, error) { return nil, errors.New("no agent") },
		}

		_, err := d.Discover(context.Background(), privateKeyPaste[:0]+"")
		// Empty explicit key: the chain falls to git config, whose value is the
		// paste. Same rule applies — user.signingkey is user-supplied too.
		assertRedacted(t, err)
	})

	t.Run("an ordinary value is still named back", func(t *testing.T) {
		ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"someone@example.com"}}
		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
			DialAgent: func() (agent.Agent, error) { return ag, nil },
			ReadFile:  func(string) ([]byte, error) { return nil, errors.New("no such file") },
		}

		_, err := d.Discover(context.Background(), "nobody@example.org")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nobody@example.org",
			"redaction must not blind the ordinary case — naming the value back is the point: %q", err.Error())
	})
}
