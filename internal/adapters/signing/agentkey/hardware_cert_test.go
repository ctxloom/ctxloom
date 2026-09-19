package agentkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// skEd25519Wire is the sk-ssh-ed25519@openssh.com public-key wire format
// (PROTOCOL.u2f): algorithm name, the ed25519 public key, and the relying
// party ("application") the token was enrolled against.
type skEd25519Wire struct {
	Name        string
	PubKey      []byte
	Application string
}

// newSKEd25519PublicKey builds a real, parseable sk-ssh-ed25519 public key —
// the type a FIDO/hardware token presents. No token is involved: only the
// public half's ENCODING matters to IsHardwareBacked, which reads the type.
func newSKEd25519PublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.ParsePublicKey(ssh.Marshal(skEd25519Wire{
		Name:        ssh.KeyAlgoSKED25519,
		PubKey:      pub,
		Application: "ssh:",
	}))
	require.NoError(t, err)
	require.Equal(t, ssh.KeyAlgoSKED25519, key.Type(), "fixture must actually be a hardware key type")
	return key
}

// certOver wraps inner in a signed OpenSSH user certificate, the way a
// CA-issued identity arrives in ssh-agent.
func certOver(t *testing.T, inner ssh.PublicKey) *ssh.Certificate {
	t.Helper()
	ca, _ := newTestIdentity(t, "ca")
	cert := &ssh.Certificate{
		Key:             inner,
		CertType:        ssh.UserCert,
		KeyId:           "hardware-identity",
		ValidPrincipals: []string{"someone"},
		ValidBefore:     ssh.CertTimeInfinity,
	}
	require.NoError(t, cert.SignCert(rand.Reader, ca))
	return cert
}

// certTypedKey is an ssh.PublicKey that REPORTS a certificate algorithm name
// without being an *ssh.Certificate — the shape IsHardwareBacked must also
// survive, since it is handed an interface and does not own its concrete type.
type certTypedKey struct {
	ssh.PublicKey
	typ string
}

func (k certTypedKey) Type() string { return k.typ }

// TestIsHardwareBacked_Certificate pins that the hardware posture is read from
// the key the certificate WRAPS, not from the certificate's own algorithm name.
//
// The defect this pins: the switch matched only the bare sk-* algorithms, but
// an identity loaded into ssh-agent as a certificate presents
// "sk-ssh-ed25519-cert-v01@openssh.com". A genuine FIDO token therefore read
// as a software key, and `ctxloom review` printed the "your approval key is a
// software key held in ssh-agent" warning to someone holding hardware — a
// posture indicator stating the opposite of the truth.
func TestIsHardwareBacked_Certificate(t *testing.T) {
	hw := newSKEd25519PublicKey(t)
	soft, _ := newTestIdentity(t, "software")

	assert.True(t, IsHardwareBacked(hw), "bare sk-ssh-ed25519 is hardware")

	hwCert := certOver(t, hw)
	require.Equal(t, ssh.CertAlgoSKED25519v01, hwCert.Type(), "fixture must present the CERT algorithm name")
	assert.True(t, IsHardwareBacked(hwCert),
		"a hardware key loaded as a certificate is still hardware-backed")

	assert.True(t, IsHardwareBacked(certTypedKey{PublicKey: hw, typ: ssh.CertAlgoSKED25519v01}),
		"a key reporting the sk cert algorithm is hardware-backed even when it is not an *ssh.Certificate")

	// The correction must not run the other way: wrapping a software key in a
	// certificate never promotes it.
	softCert := certOver(t, soft.PublicKey())
	require.Equal(t, ssh.CertAlgoED25519v01, softCert.Type())
	assert.False(t, IsHardwareBacked(softCert),
		"a certificate over a software key is still software — the warning must stand")
}
