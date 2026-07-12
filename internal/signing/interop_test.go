package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// =============================================================================
// Interop with real OpenSSH — this is the actual contract. If our verifier
// rejects a signature real ssh-keygen produced, we are wrong, not ssh-keygen.
//
// FORWARD direction (ssh-keygen signs, we verify) runs unconditionally
// against goldens committed under testdata/golden/ + testdata/keys/ — these
// were generated once with `ssh-keygen -Y sign` (see the shell transcript in
// the S2 implementation notes) and require no openssh binary or network at
// test time, matching the spec's "verification must be pure Go, offline, and
// in-process" requirement.
//
// REVERSE direction (we sign, ssh-keygen verifies) shells out to ssh-keygen
// at test time and is skipped if the binary isn't on PATH — signing MAY use
// external tooling for exotic key types per spec §11A.2; only verification
// must never shell out, and this test verifies OUR signing output, not our
// verification path.
// =============================================================================

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	require.NoError(t, err)
	return b
}

func readPub(t *testing.T, name string) ssh.PublicKey {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "keys", name))
	require.NoError(t, err)
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	require.NoError(t, err)
	return pub
}

func TestInterop_VerifiesRealSSHKeygenPublisherSignature(t *testing.T) {
	payload := readGolden(t, "bundle_publisher_payload.yaml")
	armored := readGolden(t, "bundle_publisher_payload.yaml.sig")
	pub := readPub(t, "publisher_ed25519.pub")

	err := Verify(payload, armored, pub, "publish.v1.ctxloom.dev")
	require.NoError(t, err, "our Verify must accept a signature produced by real `ssh-keygen -Y sign`")
}

func TestInterop_VerifiesRealSSHKeygenCountersignSignature(t *testing.T) {
	// This golden's content bytes are the OUTPUT of our own
	// ApproveCountersignPayload — i.e. this proves ssh-keygen and our code
	// agree on the framed bytes, not just on raw file bytes.
	payload := readGolden(t, "countersign_approve_payload.bin")
	armored := readGolden(t, "countersign_approve_payload.bin.sig")
	pub := readPub(t, "approver_ed25519.pub")

	err := Verify(payload, armored, pub, "approve.v1.ctxloom.dev")
	require.NoError(t, err)

	// Cross-check: the golden payload bytes are exactly what
	// ApproveCountersignPayload produces for the same inputs — pinning that
	// the golden wasn't hand-edited out of sync with the framing function.
	rebuilt := ApproveCountersignPayload(
		KindFragments,
		"acme-tools#fragments/go-testing",
		FormRaw,
		[]byte("Use table-driven tests in Go.\n"),
	)
	require.Equal(t, payload, rebuilt)
}

func TestInterop_RejectsGoldenUnderWrongNamespace(t *testing.T) {
	payload := readGolden(t, "bundle_publisher_payload.yaml")
	armored := readGolden(t, "bundle_publisher_payload.yaml.sig")
	pub := readPub(t, "publisher_ed25519.pub")

	err := Verify(payload, armored, pub, "approve.v1.ctxloom.dev")
	require.Error(t, err)
}

// requireSSHKeygen skips the test if the real openssh ssh-keygen binary
// isn't on PATH. Signing MAY shell out for exotic key types (spec §11A.2);
// this test exercises that allowance to prove the reverse-interop direction,
// not our verification path (which never shells out — see sign.go).
func requireSSHKeygen(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not on PATH; skipping reverse-interop check")
	}
	return path
}

func TestInterop_RealSSHKeygenVerifiesOurPublisherSignature(t *testing.T) {
	keygen := requireSSHKeygen(t)
	dir := t.TempDir()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	payload := []byte("bundle bytes signed by ctxloom, verified by real ssh-keygen\n")
	armored, err := Sign(payload, signer, "publish.v1.ctxloom.dev")
	require.NoError(t, err)

	payloadPath := filepath.Join(dir, "payload.txt")
	sigPath := payloadPath + ".sig"
	require.NoError(t, os.WriteFile(payloadPath, payload, 0o644))
	require.NoError(t, os.WriteFile(sigPath, armored, 0o644))

	pubOpenSSH := ssh.MarshalAuthorizedKey(signer.PublicKey())
	allowedSignersPath := filepath.Join(dir, "allowed_signers")
	line := append([]byte("ctxloom-test@ctxloom.dev namespaces=\"publish.v1.ctxloom.dev\" "), pubOpenSSH...)
	require.NoError(t, os.WriteFile(allowedSignersPath, line, 0o644))

	cmd := exec.Command(keygen, "-Y", "verify",
		"-f", allowedSignersPath,
		"-I", "ctxloom-test@ctxloom.dev",
		"-n", "publish.v1.ctxloom.dev",
		"-s", sigPath,
	)
	stdin, err := os.Open(payloadPath)
	require.NoError(t, err)
	defer stdin.Close()
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "real ssh-keygen -Y verify must accept a signature we produced: %s", out)
}

func TestInterop_RealSSHKeygenVerifiesOurCountersignSignature(t *testing.T) {
	keygen := requireSSHKeygen(t)
	dir := t.TempDir()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	payload := ApproveCountersignPayload(KindSkills, "my-tools#skills/reviewer", FormDistilled, []byte("distilled skill body"))
	armored, err := Sign(payload, signer, "approve.v1.ctxloom.dev")
	require.NoError(t, err)

	payloadPath := filepath.Join(dir, "payload.bin")
	sigPath := payloadPath + ".sig"
	require.NoError(t, os.WriteFile(payloadPath, payload, 0o644))
	require.NoError(t, os.WriteFile(sigPath, armored, 0o644))

	pubOpenSSH := ssh.MarshalAuthorizedKey(signer.PublicKey())
	allowedSignersPath := filepath.Join(dir, "allowed_signers")
	line := append([]byte("ctxloom-test@ctxloom.dev namespaces=\"approve.v1.ctxloom.dev\" "), pubOpenSSH...)
	require.NoError(t, os.WriteFile(allowedSignersPath, line, 0o644))

	cmd := exec.Command(keygen, "-Y", "verify",
		"-f", allowedSignersPath,
		"-I", "ctxloom-test@ctxloom.dev",
		"-n", "approve.v1.ctxloom.dev",
		"-s", sigPath,
	)
	stdin, err := os.Open(payloadPath)
	require.NoError(t, err)
	defer stdin.Close()
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "real ssh-keygen -Y verify must accept a countersign payload we produced: %s", out)
}
