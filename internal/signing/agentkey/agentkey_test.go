package agentkey

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// newTestIdentity generates a fresh ed25519 keypair, an ssh.Signer over it,
// and its authorized-keys-format public line (what git config
// user.signingkey or a --key path would contain).
func newTestIdentity(t *testing.T, comment string) (ssh.Signer, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	line := string(ssh.MarshalAuthorizedKey(sshPub))
	if comment != "" {
		line = line[:len(line)-1] + " " + comment + "\n"
	}
	return signer, line
}

// fakeAgent is an in-process agent.Agent that only implements what
// Discoverer actually calls (Signers, List), so tests don't need a real
// ssh-agent socket. Comments are tracked in parallel so
// candidatesFromSigners has something to report.
type fakeAgent struct {
	signers  []ssh.Signer
	comments []string
	// signersErr, when set, makes Signers() fail — simulating an ssh-agent
	// RPC/protocol failure (agent locked via `ssh-add -x`, a wedged socket)
	// distinct from "the agent is reachable but does not hold this key"
	// (U135-F01/F03).
	signersErr error
}

func (f *fakeAgent) List() ([]*agent.Key, error) {
	out := make([]*agent.Key, len(f.signers))
	for i, s := range f.signers {
		out[i] = &agent.Key{
			Format:  s.PublicKey().Type(),
			Blob:    s.PublicKey().Marshal(),
			Comment: f.comments[i],
		}
	}
	return out, nil
}
func (f *fakeAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	panic("not used by Discoverer")
}
func (f *fakeAgent) Add(k agent.AddedKey) error     { return errors.New("not used") }
func (f *fakeAgent) Remove(key ssh.PublicKey) error { return errors.New("not used") }
func (f *fakeAgent) RemoveAll() error               { return errors.New("not used") }
func (f *fakeAgent) Lock(passphrase []byte) error   { return errors.New("not used") }
func (f *fakeAgent) Unlock(passphrase []byte) error { return errors.New("not used") }
func (f *fakeAgent) Signers() ([]ssh.Signer, error) {
	if f.signersErr != nil {
		return nil, f.signersErr
	}
	return f.signers, nil
}

func discovererWithAgent(ag agent.Agent, gitSigningKey string, gitErr error) *Discoverer {
	return &Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) {
			if gitErr != nil {
				return "", false, gitErr
			}
			if key != "user.signingkey" {
				return "", false, nil
			}
			return gitSigningKey, gitSigningKey != "", nil
		},
		DialAgent: func() (agent.Agent, error) { return ag, nil },
		ReadFile:  func(path string) ([]byte, error) { return nil, fmt.Errorf("no such file: %s", path) },
	}
}

func TestDiscover_GitSigningKeyResolvesWithNoCtxloomConfig(t *testing.T) {
	signer, pubLine := newTestIdentity(t, "alice@laptop")
	other, otherLine := newTestIdentity(t, "other")
	ag := &fakeAgent{signers: []ssh.Signer{other, signer}, comments: []string{"other", "alice@laptop"}}
	_ = otherLine

	d := discovererWithAgent(ag, pubLine, nil)

	got, err := d.Discover(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
	assert.Equal(t, "git config user.signingkey", got.Source)
}

func TestDiscover_SoleAgentIdentity_NoGitConfig(t *testing.T) {
	signer, _ := newTestIdentity(t, "solo")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"solo"}}
	d := discovererWithAgent(ag, "", nil)

	got, err := d.Discover(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
	assert.Equal(t, "ssh-agent (sole identity)", got.Source)
}

func TestDiscover_Ambiguous_MultipleAgentIdentitiesNoGitConfig(t *testing.T) {
	s1, _ := newTestIdentity(t, "one")
	s2, _ := newTestIdentity(t, "two")
	ag := &fakeAgent{signers: []ssh.Signer{s1, s2}, comments: []string{"one", "two"}}
	d := discovererWithAgent(ag, "", nil)

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var ambigErr *AmbiguousKeyError
	require.ErrorAs(t, err, &ambigErr)
	assert.Len(t, ambigErr.Candidates, 2)
	// Actionable: the error text names a concrete fix.
	assert.Contains(t, err.Error(), "config set sign.key")
	assert.Contains(t, err.Error(), "git config")
}

func TestDiscover_Empty_NoAgentNoGitConfig(t *testing.T) {
	d := &Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return nil, errors.New("SSH_AUTH_SOCK is not set") },
		ReadFile:  func(path string) ([]byte, error) { return nil, errors.New("nope") },
	}

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
	assert.Contains(t, err.Error(), "no signing key found")
	assert.Contains(t, err.Error(), "ssh-add")
	assert.Contains(t, err.Error(), "--no-sign")
}

func TestDiscover_ExplicitKeyWinsOverGitConfigAndAgentAmbiguity(t *testing.T) {
	wanted, wantedLine := newTestIdentity(t, "wanted")
	decoy1, _ := newTestIdentity(t, "decoy1")
	decoy2, decoy2Line := newTestIdentity(t, "decoy2")
	ag := &fakeAgent{signers: []ssh.Signer{decoy1, wanted, decoy1}, comments: []string{"d1", "wanted", "d1"}}
	_ = decoy2Line
	_ = decoy2

	d := discovererWithAgent(ag, "", nil) // git config unset; agent has >1 identity (would be ambiguous)
	d.ReadFile = func(path string) ([]byte, error) {
		if path == "/keys/wanted.pub" {
			return []byte(wantedLine), nil
		}
		return nil, fmt.Errorf("no such file: %s", path)
	}

	got, err := d.Discover(context.Background(), "/keys/wanted.pub")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(wanted.PublicKey()), got.Fingerprint)
	assert.Equal(t, "--key", got.Source)
}

// U135-F02: `user.signingkey` conventionally names the PRIVATE key path in
// some real setups (the package's own resolvePublicKey comment names this
// case), with the public key living alongside it as "<path>.pub". The
// .pub-sibling fallback lived ONLY inside the `ReadFile(path)` FAILURE
// branch, so it could never fire here: the private-key path reads
// successfully (it exists!), ssh.ParseAuthorizedKey then fails on private-key
// bytes, and the whole zero-config git path broke for this common setup.
func TestDiscover_GitSigningKeyNamesPrivateKeyPath_FallsBackToPubSibling(t *testing.T) {
	wanted, wantedLine := newTestIdentity(t, "alice@laptop")
	ag := &fakeAgent{signers: []ssh.Signer{wanted}, comments: []string{"alice@laptop"}}

	privatePath := "/home/alice/.ssh/id_ed25519"
	d := discovererWithAgent(ag, privatePath, nil)
	d.ReadFile = func(path string) ([]byte, error) {
		switch path {
		case privatePath:
			// The private key file readably exists at this path — reading it
			// succeeds, so the OLD fallback (gated on a read FAILURE) never
			// triggered.
			return []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfakefakefake\n-----END OPENSSH PRIVATE KEY-----\n"), nil
		case privatePath + ".pub":
			return []byte(wantedLine), nil
		default:
			return nil, fmt.Errorf("no such file: %s", path)
		}
	}

	got, err := d.Discover(context.Background(), "")
	require.NoError(t, err, "user.signingkey conventionally names the PRIVATE key path in some setups; the .pub sibling must still be tried")
	assert.Equal(t, ssh.FingerprintSHA256(wanted.PublicKey()), got.Fingerprint)
}

func TestDiscover_GitNamesKeyNotLoadedInAgent_HardError(t *testing.T) {
	_, namedLine := newTestIdentity(t, "named-but-absent")
	other, _ := newTestIdentity(t, "present")
	ag := &fakeAgent{signers: []ssh.Signer{other}, comments: []string{"present"}}

	d := discovererWithAgent(ag, namedLine, nil)

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
	assert.Contains(t, err.Error(), "no signing key found")
}

// U135-F01(a): an ssh-agent RPC/protocol failure (agent locked, wedged
// socket) while resolving a git-named key must NOT be reported as "it is not
// loaded in ssh-agent — ssh-add it" — that message tells the user to load a
// key that IS loaded; the agent just could not be asked. The real cause
// (findByPublicKey's listing failure) must reach the user.
func TestDiscover_GitSigningKey_AgentListingFailure_NotMisreportedAsNotLoaded(t *testing.T) {
	_, namedLine := newTestIdentity(t, "named")
	ag := &fakeAgent{signersErr: errAgentLockedSentinel}

	d := discovererWithAgent(ag, namedLine, nil)

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
	assert.Contains(t, noKeyErr.Detail, "agent locked",
		"the real listing failure must reach the user, not a generic 'not loaded' guess")
	assert.NotContains(t, noKeyErr.Detail, "ssh-add it",
		"telling the user to ssh-add a key that IS loaded (the agent just could not be asked) sends them to fix the wrong thing")
}

// U135-F01(c): the same listing-failure distinction for step 3 of the chain
// (the sole-agent-identity fallback, no git config, no --key). The underlying
// cause must also be reachable via errors.Is/As, not flattened into a Detail
// string nobody outside this package can match on.
var errAgentLockedSentinel = errors.New("agent locked")

func TestDiscover_SoleAgentIdentity_AgentListingFailure_SurfacesRealCause(t *testing.T) {
	ag := &fakeAgent{signersErr: errAgentLockedSentinel}
	d := discovererWithAgent(ag, "", nil)

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
	assert.Contains(t, noKeyErr.Detail, "agent locked")
	assert.ErrorIs(t, err, errAgentLockedSentinel,
		"the underlying cause must be reachable via errors.Is, not flattened into a string nobody can match on")
}

func TestDiscover_NoAgentRunning_GitNamesKey_HardError(t *testing.T) {
	_, namedLine := newTestIdentity(t, "named")
	d := &Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) {
			return namedLine, true, nil
		},
		DialAgent: func() (agent.Agent, error) { return nil, errors.New("SSH_AUTH_SOCK is not set") },
		ReadFile:  func(path string) ([]byte, error) { return nil, errors.New("nope") },
	}

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
}

// TestDiscover_ExplicitFingerprintMatchesAgentIdentity exercises the
// SHA256:<fp> spelling of --key.
func TestDiscover_ExplicitFingerprintMatchesAgentIdentity(t *testing.T) {
	signer, _ := newTestIdentity(t, "fp-target")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"fp-target"}}
	d := discovererWithAgent(ag, "", nil)

	fp := ssh.FingerprintSHA256(signer.PublicKey())
	got, err := d.Discover(context.Background(), fp)
	require.NoError(t, err)
	assert.Equal(t, fp, got.Fingerprint)
}

// TestDiscover_ExplicitKeyName_ExactCommentMatch exercises the new fallback:
// --key given a name that exactly equals an agent identity's comment.
func TestDiscover_ExplicitKeyName_ExactCommentMatch(t *testing.T) {
	signer, _ := newTestIdentity(t, "ben@abbitt.me")
	other, _ := newTestIdentity(t, "other@example.com")
	ag := &fakeAgent{signers: []ssh.Signer{other, signer}, comments: []string{"other@example.com", "ben@abbitt.me"}}
	d := discovererWithAgent(ag, "", nil)

	got, err := d.Discover(context.Background(), "ben@abbitt.me")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
	assert.Equal(t, "--key", got.Source)
}

// TestDiscover_ExplicitKeyName_SubstringMatch: a partial name ("ben@abbitt")
// matches the fuller comment ("ben@abbitt.me").
func TestDiscover_ExplicitKeyName_SubstringMatch(t *testing.T) {
	signer, _ := newTestIdentity(t, "ben@abbitt.me")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"ben@abbitt.me"}}
	d := discovererWithAgent(ag, "", nil)

	got, err := d.Discover(context.Background(), "ben@abbitt")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
}

// TestDiscover_ExplicitKeyName_CaseInsensitive: the match is
// case-insensitive both on the query and the stored comment.
func TestDiscover_ExplicitKeyName_CaseInsensitive(t *testing.T) {
	signer, _ := newTestIdentity(t, "Ben@Abbitt.ME")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"Ben@Abbitt.ME"}}
	d := discovererWithAgent(ag, "", nil)

	got, err := d.Discover(context.Background(), "ben@abbitt")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
}

// TestDiscover_ExplicitKeyName_AmbiguousSubstring_ErrorListsBoth: a name
// substring matching 2+ agent identities is a hard error, never a guess, and
// lists every match with its fingerprint and type so the user can
// disambiguate.
func TestDiscover_ExplicitKeyName_AmbiguousSubstring_ErrorListsBoth(t *testing.T) {
	s1, _ := newTestIdentity(t, "ben@abbitt.me")
	s2, _ := newTestIdentity(t, "ben+ctxloom@abbitt.me")
	ag := &fakeAgent{signers: []ssh.Signer{s1, s2}, comments: []string{"ben@abbitt.me", "ben+ctxloom@abbitt.me"}}
	d := discovererWithAgent(ag, "", nil)

	_, err := d.Discover(context.Background(), "ben")
	require.Error(t, err)
	var ambigErr *AmbiguousKeyNameError
	require.ErrorAs(t, err, &ambigErr)
	assert.Len(t, ambigErr.Candidates, 2)
	assert.Contains(t, err.Error(), `"ben"`)
	assert.Contains(t, err.Error(), ssh.FingerprintSHA256(s1.PublicKey()))
	assert.Contains(t, err.Error(), ssh.FingerprintSHA256(s2.PublicKey()))
	assert.Contains(t, err.Error(), "ben@abbitt.me")
	assert.Contains(t, err.Error(), "ben+ctxloom@abbitt.me")
	assert.Contains(t, err.Error(), "fingerprint")
}

// TestDiscover_ExplicitKeyName_NoMatch_ClearError: a name that matches no
// agent identity comment (and isn't a fingerprint or file) is a clear,
// distinguishable error rather than a silent fallback.
func TestDiscover_ExplicitKeyName_NoMatch_ClearError(t *testing.T) {
	signer, _ := newTestIdentity(t, "ben@abbitt.me")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"ben@abbitt.me"}}
	d := discovererWithAgent(ag, "", nil)

	_, err := d.Discover(context.Background(), "nobody-here")
	require.Error(t, err)
	var ambigErr *AmbiguousKeyNameError
	assert.False(t, errors.As(err, &ambigErr), "a zero-match name must not be reported as ambiguous")
	assert.Contains(t, err.Error(), "nobody-here")
}

// U135-F01(b): a --key NAME that falls through to the comment-matching
// fallback, on an agent whose Signers() listing itself fails (not merely
// "no comment matched"), must surface that real cause — resolveByComment's
// "listing ssh-agent identities: <rpc err>" was silently dropped by
// resolveExplicit unless it was an *AmbiguousKeyNameError, replaced by the
// generic "not a recognized fingerprint, public key, or ssh-agent key name".
func TestDiscover_ExplicitKeyName_AgentListingFailure_SurfacesRealCause(t *testing.T) {
	ag := &fakeAgent{signersErr: errAgentLockedSentinel}
	d := discovererWithAgent(ag, "", nil)

	_, err := d.Discover(context.Background(), "nobody-here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent locked",
		"the real ssh-agent listing failure must reach the user, not just a generic 'not recognized' message")
}

// TestDiscover_ExplicitKeyName_EmptyCommentNeverMatched: a key with no
// comment must never match, even against an empty-ish query, since an
// unconditional substring match against "" would otherwise match everything.
func TestDiscover_ExplicitKeyName_EmptyCommentNeverMatched(t *testing.T) {
	signer, _ := newTestIdentity(t, "")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{""}}
	d := discovererWithAgent(ag, "", nil)

	_, err := d.Discover(context.Background(), "anything")
	require.Error(t, err)
	var ambigErr *AmbiguousKeyNameError
	assert.False(t, errors.As(err, &ambigErr))
}

// TestDiscover_ExplicitFingerprint_StillWinsOverNameFallback is a regression
// test for the fallback ordering: a SHA256: fingerprint must resolve via
// findByFingerprint, never fall through to comment matching, even though
// nothing here changed that path — it pins step (a) of the resolveExplicit
// chain against being reordered behind the new step (c).
func TestDiscover_ExplicitFingerprint_StillWinsOverNameFallback(t *testing.T) {
	signer, _ := newTestIdentity(t, "SHA256-lookalike-comment")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"SHA256-lookalike-comment"}}
	d := discovererWithAgent(ag, "", nil)

	fp := ssh.FingerprintSHA256(signer.PublicKey())
	got, err := d.Discover(context.Background(), fp)
	require.NoError(t, err)
	assert.Equal(t, fp, got.Fingerprint)
	assert.Equal(t, "--key", got.Source)
}

// TestDiscover_ExplicitFilePath_StillWinsOverNameFallback is a regression
// test pinning step (b): a --key value that resolves as a real public-key
// file must resolve via findByPublicKey, never fall through to comment
// matching, even when its text would also match some agent comment.
func TestDiscover_ExplicitFilePath_StillWinsOverNameFallback(t *testing.T) {
	wanted, wantedLine := newTestIdentity(t, "wanted")
	decoy, _ := newTestIdentity(t, "/keys/wanted.pub") // comment literally contains the path text
	ag := &fakeAgent{signers: []ssh.Signer{decoy, wanted}, comments: []string{"/keys/wanted.pub", "wanted"}}
	d := discovererWithAgent(ag, "", nil)
	d.ReadFile = func(path string) ([]byte, error) {
		if path == "/keys/wanted.pub" {
			return []byte(wantedLine), nil
		}
		return nil, fmt.Errorf("no such file: %s", path)
	}

	got, err := d.Discover(context.Background(), "/keys/wanted.pub")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(wanted.PublicKey()), got.Fingerprint, "the file must resolve, not the decoy whose comment matches the path text")
}

func TestIsHardwareBacked(t *testing.T) {
	signer, _ := newTestIdentity(t, "plain")

	assert.False(t, IsHardwareBacked(signer.PublicKey()), "a plain ed25519 key is software, not hardware")
	assert.False(t, IsHardwareBacked(nil))
}

// TestExpandHome pins U135-F13: expandHome now uses filepath.Join, replacing a
// hand-rolled twin whose stated rationale ("avoids importing path/filepath for
// one call site twice") was false. The two differ on real inputs — the twin
// produced a TRAILING SEPARATOR for a bare "~" (home + sep + "") and never
// cleaned the result — so pin the shape the caller actually needs: a clean,
// separator-correct absolute path, and a non-tilde path returned untouched.
func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir in this environment: %v", err)
	}

	got, err := expandHome("~")
	require.NoError(t, err)
	if got != home {
		t.Errorf("expandHome(%q) = %q, want %q (no trailing separator)", "~", got, home)
	}
	got, err = expandHome("~/.ssh/id_ed25519")
	require.NoError(t, err)
	if want := filepath.Join(home, ".ssh/id_ed25519"); got != want {
		t.Errorf("expandHome(~/.ssh/id_ed25519) = %q, want %q", got, want)
	}
	for _, p := range []string{"/abs/path/key", "relative/key", "", "~notauser/key"} {
		got, err := expandHome(p)
		require.NoError(t, err, "a path with no leading ~ never consults $HOME")
		if got != p {
			t.Errorf("expandHome(%q) = %q, want it returned untouched", p, got)
		}
	}
}
