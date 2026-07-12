package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// =============================================================================
// TRANSPORT-AGNOSTIC INVARIANT (spec §3.0): "A signed artifact verifies
// identically, byte-for-byte, regardless of how it arrived." This test signs
// ONCE and verifies the SAME payload delivered three ways: (a) read back out
// of a real git object store, (b) carried as base64 inside a JSON envelope
// on "stdout" (an in-memory buffer standing in for a companion's stdout —
// the encoding step is what's under test, not the pipe), and (c) a plain
// file on disk. A carrier that works for one and not another is a failed
// design (implementer trap, spec §14 "coupling the signature to the
// transport").
// =============================================================================

// loadoutEnvelope mirrors the §4.3 companion-loadout JSON envelope shape.
// Only the fields this test exercises are modeled.
type loadoutEnvelope struct {
	Contract  string `json:"contract"`
	Bundle    string `json:"bundle"` // base64(std, padded) of the exact bytes
	Signature string `json:"signature"`
	Signer    string `json:"signer"` // advisory only — never trusted (spec §4.3, trap #3)
}

func TestTransportAgnostic_SameSignatureVerifiesThroughAllThreeChannels(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	const namespace = "publish.v1.ctxloom.dev"
	original := []byte("version: \"1.0\"\nfragments:\n  x:\n    content: hello\n")

	// Sign ONCE.
	armored, err := Sign(original, signer, namespace)
	require.NoError(t, err)

	// --- Channel (a): git object store ---------------------------------
	// A real git blob write/read round trip (spec §4.1's carrier), proving
	// the signature covers content bytes and knows nothing about git SHAs.
	gitDir := t.TempDir()
	runGit := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = gitDir
		out, err := cmd.Output()
		require.NoError(t, err, "git %v", args)
		return out
	}
	runGit("init", "-q")
	hashObjectCmd := exec.Command("git", "hash-object", "-w", "--stdin")
	hashObjectCmd.Dir = gitDir
	hashObjectCmd.Stdin = bytesReader(original)
	shaOut, err := hashObjectCmd.Output()
	require.NoError(t, err)
	blobSHA := trimNewline(shaOut)
	fromGit := runGit("cat-file", "-p", blobSHA)

	// --- Channel (b): JSON envelope on "stdout" -------------------------
	envelope := loadoutEnvelope{
		Contract:  "ctxloom-loadout/1",
		Bundle:    base64.StdEncoding.EncodeToString(original),
		Signature: string(armored),
		Signer:    "releases@ctxloom.dev", // advisory; verification never reads this
	}
	envelopeJSON, err := json.Marshal(envelope)
	require.NoError(t, err)

	var decodedEnvelope loadoutEnvelope
	require.NoError(t, json.Unmarshal(envelopeJSON, &decodedEnvelope))
	fromJSON, err := base64.StdEncoding.DecodeString(decodedEnvelope.Bundle)
	require.NoError(t, err)
	armoredFromJSON := []byte(decodedEnvelope.Signature)

	// --- Channel (c): plain file -----------------------------------------
	fileDir := t.TempDir()
	filePath := filepath.Join(fileDir, "bundle.yaml")
	require.NoError(t, os.WriteFile(filePath, original, 0o644))
	fromFile, err := os.ReadFile(filePath)
	require.NoError(t, err)

	// All three channels must have carried byte-IDENTICAL content.
	require.Equal(t, original, fromGit, "git object store must round-trip bytes verbatim")
	require.Equal(t, original, fromJSON, "JSON envelope must round-trip bytes verbatim")
	require.Equal(t, original, fromFile, "plain file must round-trip bytes verbatim")

	pub := signer.PublicKey()

	// The SAME signature blob (armored) must verify against bytes recovered
	// from every channel, under the same namespace, with the same verdict.
	require.NoError(t, Verify(fromGit, armored, pub, namespace), "git channel")
	require.NoError(t, Verify(fromJSON, armoredFromJSON, pub, namespace), "JSON envelope channel")
	require.NoError(t, Verify(fromFile, armored, pub, namespace), "plain file channel")

	// And the signature bytes themselves are identical across channels —
	// nothing about the channel mutated the signature blob either.
	require.Equal(t, armored, armoredFromJSON)
}

func bytesReader(b []byte) *os.File {
	// os/exec wants an io.Reader for Stdin; use an in-memory pipe fed by a
	// goroutine rather than a real temp file, to keep this channel distinct
	// from channel (c).
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	go func() {
		_, _ = w.Write(b)
		_ = w.Close()
	}()
	return r
}

func trimNewline(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
