// Package agentkey resolves an ssh.Signer to countersign with, from a running
// ssh-agent over SSH_AUTH_SOCK, and answers the one honest posture question
// ctxloom can detect about that key: is it hardware-backed (spec §9.1.2).
//
// TODO(unify with S7 signer key discovery): this is a MINIMAL stand-in for
// the full resolution chain the signature-envelope spec describes (spec
// §7A.4: git config user.signingkey → ssh-agent → sign.key config → --key,
// with a named-choice error on ambiguity). That full chain, plus `ctxloom
// sign` / `ctxloom signer`, is owned by a parallel slice (S7,
// internal/cli/sign.go + internal/cli/signer.go) working concurrently with
// this one. S6 (the countersignature store + `ctxloom review`) only needs
// "find a key to countersign with" and deliberately does not create those
// filenames to avoid colliding with S7's CLI surface — this package is where
// that minimal need lives until S7 lands and the two are unified. Do not grow
// this into the full chain; extend S7's resolver instead and have review call
// it once it exists.
package agentkey

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ErrNoAgent reports that no ssh-agent is reachable — SSH_AUTH_SOCK is unset,
// or the socket cannot be dialed. Not an error condition callers should
// surface as a crash: it is one of the two real "no key" cases the spec's
// degraded path (§9.5) must handle gracefully.
var ErrNoAgent = errors.New("no ssh-agent reachable (SSH_AUTH_SOCK not set, or not connectable)")

// ErrNoIdentities reports that the agent is reachable but holds no keys.
var ErrNoIdentities = errors.New("ssh-agent holds no identities")

// AmbiguousError reports that more than one identity is available and none
// was preferred; callers must not guess (spec §7A.4) — the offending
// identities are attached so a caller can present the choice.
type AmbiguousError struct {
	Identities []Identity
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("ssh-agent holds %d identities; specify which one to sign with", len(e.Identities))
}

// Identity is one usable signing identity reachable through the agent.
type Identity struct {
	Signer  ssh.Signer
	Comment string
}

// Fingerprint returns the identity's SHA256 fingerprint (ssh-keygen -l
// format), the value a human compares out of band and the value a
// preferred-key selector matches against.
func (id Identity) Fingerprint() string {
	return ssh.FingerprintSHA256(id.Signer.PublicKey())
}

// Client wraps a live ssh-agent connection.
type Client struct {
	conn net.Conn
	ag   agent.ExtendedAgent
}

// Dial opens an ssh-agent connection over the given UNIX socket path.
func Dial(socketPath string) (*Client, error) {
	if socketPath == "" {
		return nil, ErrNoAgent
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoAgent, err)
	}
	return &Client{conn: conn, ag: agent.NewClient(conn)}, nil
}

// DialEnv opens an ssh-agent connection using SSH_AUTH_SOCK from the process
// environment — the zero-config common case.
func DialEnv() (*Client, error) {
	return Dial(os.Getenv("SSH_AUTH_SOCK"))
}

// Close releases the underlying connection. Safe on a nil Client.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Identities lists every identity the agent currently holds, each as a
// ready-to-use ssh.Signer (golang.org/x/crypto/ssh/agent's client signers
// already satisfy ssh.Signer — no adapter needed, see
// internal/signing/agent_signer_test.go).
func (c *Client) Identities() ([]Identity, error) {
	if c == nil {
		return nil, ErrNoAgent
	}
	signers, err := c.ag.Signers()
	if err != nil {
		return nil, err
	}
	keys, _ := c.ag.List() // best-effort: comments are display-only
	commentFor := func(pub ssh.PublicKey) string {
		for _, k := range keys {
			if bytes.Equal(k.Marshal(), pub.Marshal()) {
				return k.Comment
			}
		}
		return ""
	}
	out := make([]Identity, 0, len(signers))
	for _, s := range signers {
		out = append(out, Identity{Signer: s, Comment: commentFor(s.PublicKey())})
	}
	return out, nil
}

// Resolve picks the signing identity to countersign with, over an agent
// dialed at socketPath. If preferredFingerprint is non-empty, it selects that
// exact identity (SHA256 fingerprint match) or errors if absent. Otherwise:
// exactly one identity resolves unambiguously; zero is ErrNoIdentities; more
// than one is an *AmbiguousError — signing with the wrong identity produces a
// signature nobody trusts, so this never guesses (spec §7A.4).
func Resolve(socketPath, preferredFingerprint string) (ssh.Signer, error) {
	c, err := Dial(socketPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()

	ids, err := c.Identities()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNoIdentities
	}
	if preferredFingerprint != "" {
		for _, id := range ids {
			if id.Fingerprint() == preferredFingerprint {
				return id.Signer, nil
			}
		}
		return nil, fmt.Errorf("preferred key %q not found among ssh-agent's identities", preferredFingerprint)
	}
	if len(ids) == 1 {
		return ids[0].Signer, nil
	}
	return nil, &AmbiguousError{Identities: ids}
}

// ResolveFromEnv is Resolve over SSH_AUTH_SOCK — the zero-config path.
func ResolveFromEnv(preferredFingerprint string) (ssh.Signer, error) {
	return Resolve(os.Getenv("SSH_AUTH_SOCK"), preferredFingerprint)
}

// IsHardwareBacked reports whether pub's key TYPE is self-identifying as
// hardware-backed (spec §9.1.2, posture P3): sk-ssh-ed25519@openssh.com or
// sk-ecdsa-sha2-nistp256@openssh.com. This is the ONLY signing posture ctxloom
// can detect honestly from the public key alone — whether a plain key is
// guarded by `ssh-add -c` (confirm-before-use) has no protocol-visible
// signal (agent.Agent.List returns key blob + comment, nothing else) and
// must be self-attested by the user, never inferred (spec §9.1.2: "I looked
// for another honest signal and there is none").
func IsHardwareBacked(pub ssh.PublicKey) bool {
	if pub == nil {
		return false
	}
	switch pub.Type() {
	case ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256:
		return true
	default:
		return false
	}
}
