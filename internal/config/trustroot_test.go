package config

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// newTestKey returns an ephemeral ed25519 public key and its authorized_keys
// line body ("ssh-ed25519 AAAA…"), for composing allowed_signers files.
func newTestKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return sshPub, string(ssh.MarshalAuthorizedKey(sshPub))
}

// A hand-edited project allowed_signers file must work with no CLI at all: the
// `ctxloom signer` porcelain is a later slice, but the file IS the trust root
// and a user (or a team, by committing it) must be able to write it today.
func TestTrustRoot_ProjectStoreIsParsedAndTrusted(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, keyLine := newTestKey(t)

	line := "bundles@ctxloom.dev namespaces=\"" + signing.NamespacePublish + "\" " + keyLine
	require.NoError(t, afero.WriteFile(fs, paths.AllowedSignersPath(".ctxloom"), []byte(line), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	decision := cfg.TrustRoot().TrustedForNamespace(pub, signing.NamespacePublish, time.Now())
	assert.True(t, decision.Trusted, "a key listed in the project allowed_signers is trusted for the namespace it lists")
	assert.Equal(t, "bundles@ctxloom.dev", decision.Principal)
}

// The namespaces= option IS the role system (spec §7): a publish-only key must
// not be able to approve, and an approve-only key must not be able to publish.
func TestTrustRoot_NamespaceScopingIsEnforced(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, keyLine := newTestKey(t)

	line := "lead@team.example namespaces=\"" + signing.NamespaceApprove + "\" " + keyLine
	require.NoError(t, afero.WriteFile(fs, paths.AllowedSignersPath(".ctxloom"), []byte(line), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}
	root := cfg.TrustRoot()

	assert.True(t, root.TrustedForNamespace(pub, signing.NamespaceApprove, time.Now()).Trusted,
		"the key is trusted for the namespace it lists")
	assert.False(t, root.TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted,
		"an approve-only key must never authorize published content (trap #3)")
}

// No allowed_signers anywhere is the overwhelmingly common case: it must be a
// quiet empty trust root, never an error, and it must trust nothing.
func TestTrustRoot_AbsentStoreTrustsNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, _ := newTestKey(t)

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	root := cfg.TrustRoot()
	require.NotNil(t, root, "an absent trust root is an empty store, never nil")
	assert.False(t, root.TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted)
}

// One malformed line must not disarm the rest of the file (ssh-keygen's own
// behavior): the good keys still load.
func TestTrustRoot_MalformedLineSkippedRestStillLoads(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, keyLine := newTestKey(t)

	content := "this-line-is-garbage-with-no-key\n" +
		"bundles@ctxloom.dev namespaces=\"" + signing.NamespacePublish + "\" " + keyLine
	require.NoError(t, afero.WriteFile(fs, paths.AllowedSignersPath(".ctxloom"), []byte(content), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	assert.True(t, cfg.TrustRoot().TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted,
		"a malformed line is skipped; the valid entries in the same file still load")
}

// --- an unreadable trust-root location must not vanish ----------------------

// denyOpenFs fails Open for one path — "permission denied" without chmod, so
// the test is deterministic and does not skip under root.
type denyOpenFs struct {
	afero.Fs
	deny string
}

func (f denyOpenFs) Open(name string) (afero.File, error) {
	if name == f.deny {
		return nil, errors.New("permission denied")
	}
	return f.Fs.Open(name)
}

// An allowed_signers file that EXISTS but cannot be opened was erased
// entirely: parseAllowedSigners returned nil, Union skipped nil, and the
// resulting trust root was byte-identical to one where the file simply did
// not exist. Every key that file listed silently stopped counting, with no
// warning and nothing on the Store to ask.
func TestTrustRoot_UnreadableStore_IsRecordedNotErased(t *testing.T) {
	base := afero.NewMemMapFs()
	path := paths.AllowedSignersPath(".ctxloom")
	require.NoError(t, afero.WriteFile(base, path, []byte("# whatever\n"), 0o644))
	fs := denyOpenFs{Fs: base, deny: path}

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}
	root := cfg.TrustRoot()

	failed := root.LoadErrors()
	require.Len(t, failed, 1, "an unreadable allowed_signers location must survive as a failed source")
	assert.Equal(t, path, failed[0].Path)
	require.Error(t, failed[0].Err)
}

// TestTrustRoot_UnreadableStore_EscalatesViaStrictness proves an unreadable
// allowed_signers is not just recorded (the prior test) but
// ESCALATED — strictness.Fail(ClassTrust, ...), matching EffectiveTrust's
// fail-closed posture for a corrupt trust store, in addition to the stderr
// warning. Without this, a `chmod 000`/EACCES/directory-in-its-place could
// disarm the whole on-disk trust root with only a line easy to miss in a
// noisy startup, no structured finding a choke owner could act on.
func TestTrustRoot_UnreadableStore_EscalatesViaStrictness(t *testing.T) {
	resetConfigStrictness(t)
	base := afero.NewMemMapFs()
	path := paths.AllowedSignersPath(".ctxloom")
	require.NoError(t, afero.WriteFile(base, path, []byte("# whatever\n"), 0o644))
	fs := denyOpenFs{Fs: base, deny: path}

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}
	mark := strictness.Checkpoint()
	cfg.TrustRoot()

	findings := strictness.Since(mark)
	require.NotEmpty(t, findings, "an unreadable allowed_signers must be escalated, not just warned to stderr")
}

// The control: an ABSENT file is the overwhelmingly common case and is not a
// failure. It must contribute nothing and record nothing — otherwise every
// fresh install reports a broken trust root.
func TestTrustRoot_AbsentStore_IsNotALoadError(t *testing.T) {
	cfg := &Config{appPaths: []string{".ctxloom"}, fs: afero.NewMemMapFs()}
	assert.Empty(t, cfg.TrustRoot().LoadErrors())
}

// The MIRROR of TestTrustRoot_UnreadableStore_IsRecordedNotErased, on the
// suppression side — and the direction of the degradation is REVERSED. An
// unreadable allowed_signers means fewer keys trusted (safe). An unreadable
// distrusted_signers means fewer SUPPRESSIONS, i.e. an embedded key the
// operator explicitly removed silently counts again: a human's "no" quietly
// reversed. It must not be silent.
func TestSuppressedEmbeddedPrincipals_UnreadableStore_IsLoud(t *testing.T) {
	base := afero.NewMemMapFs()
	path := paths.DistrustedSignersPath(".ctxloom")
	require.NoError(t, afero.WriteFile(base, path, []byte("bundles@ctxloom.dev\n"), 0o644))
	fs := denyOpenFs{Fs: base, deny: path}

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	cfg.SuppressedEmbeddedPrincipals()

	assert.Contains(t, buf.String(), "distrusted_signers",
		"an unreadable suppression file re-trusts a key the operator removed; that must be reported")
}

// --- shared-behaviour pin for the trust-root filesystem resolution ----------

// TrustRoot and SuppressedEmbeddedPrincipals both resolve their filesystem
// before touching disk. A Config built without an injected filesystem (the
// production shape) must still resolve to the OS filesystem rather than
// dereferencing a nil afero.Fs, and an INJECTED filesystem must be the only
// one either method reads. Both properties are what the resolution step buys;
// pinning them here keeps a single shared resolver honest.
func TestTrustRootFilesystemResolution_NilFSFallsBackAndInjectedFSIsHonored(t *testing.T) {
	// HOME is rooted at a temp dir because the nil-filesystem subtest falls back
	// to the REAL OS filesystem by design, and distrustedSignersPaths includes a
	// HOME-rooted path (paths.HomeDistrustedSignersPath). Without this the
	// "nothing is suppressed" assertion reads the developer's own
	// ~/.ctxloom/distrusted_signers and fails on any machine that has ever
	// suppressed a key — passing on a clean checkout and CI while being wrong.
	// Measured 2026-08-26: it returned map[ben+ctxloom@abbitt.me:true] here.
	t.Setenv("HOME", t.TempDir())

	t.Run("nil filesystem does not panic", func(t *testing.T) {
		cfg := &Config{appPaths: []string{"/nonexistent-ctxloom-project/.ctxloom"}}
		require.Nil(t, cfg.fs, "the fixture must exercise the nil-filesystem path")
		assert.NotPanics(t, func() {
			assert.NotNil(t, cfg.TrustRoot())
			assert.Empty(t, cfg.SuppressedEmbeddedPrincipals())
		})
	})

	t.Run("injected filesystem is the one read", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		_, line := newTestKey(t)
		require.NoError(t, afero.WriteFile(fs,
			paths.AllowedSignersPath(".ctxloom"),
			[]byte("pinned@example.com namespaces=\"ctxloom\" "+line+"\n"), 0o644))
		require.NoError(t, afero.WriteFile(fs,
			paths.DistrustedSignersPath(".ctxloom"),
			[]byte("suppressed@example.com\n"), 0o644))

		cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}
		assert.NotEmpty(t, cfg.TrustRoot().Entries(), "the injected allowed_signers must be read")
		assert.True(t, cfg.SuppressedEmbeddedPrincipals()["suppressed@example.com"],
			"the injected distrusted_signers must be read")
	})
}

// --- parity pin across the two signer-store path lists ----------------------

// allowedSignersPaths and distrustedSignersPaths are the SAME two-location
// shape (user store first, then project store, project skipped when it names
// the same file as the user one) over two different pairs of path builders.
// This pins that shape on BOTH so it stays one behaviour: same length, same
// order, same home/project dedup decision, for every appPaths configuration
// that matters. A divergence between the two lists — one growing a location
// the other does not, or one dropping the dedup — is the defect this guards.
func TestSignerStorePaths_AllowedAndDistrustedAgreeOnShape(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	homeApp := filepath.Join(home, AppDirName)

	cases := []struct {
		name     string
		appPaths []string
		wantLen  int
	}{
		{"no project store: user location only", nil, 1},
		{"distinct project store: both locations", []string{".ctxloom"}, 2},
		{"home-rooted project store: deduplicated to one", []string{homeApp}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{appPaths: tc.appPaths}
			allowed := cfg.allowedSignersPaths()
			distrusted := cfg.distrustedSignersPaths()

			require.Len(t, allowed, tc.wantLen)
			require.Len(t, distrusted, tc.wantLen,
				"the suppression store must list the same locations as the trust store")

			for i := range allowed {
				assert.Equal(t, filepath.Dir(allowed[i]), filepath.Dir(distrusted[i]),
					"location %d must be the same directory in both lists", i)
				assert.Equal(t, paths.AllowedSignersFileName, filepath.Base(allowed[i]))
				assert.Equal(t, paths.DistrustedSignersFileName, filepath.Base(distrusted[i]))
			}
			if tc.wantLen == 2 {
				assert.Equal(t, homeApp, filepath.Dir(allowed[0]), "the user location comes first")
			}
		})
	}
}

// A distrusted_signers file that opens and then stops PART WAY THROUGH — a
// mid-read I/O error, or a line past bufio.Scanner's 64 KiB token limit — ends
// the scan with whatever was parsed so far. The entries below the truncation
// point are revocations this process cannot see, so the trust root trusts no
// first-party signer at all until the file reads in full, and says so.
//
// The set SuppressedEmbeddedPrincipals reports is unchanged: it answers "which
// principals are named", a display question, and a partial answer to it is
// still the truth about what was read. The decision built on top of it is
// where the incompleteness has to bite.
func TestSuppressedEmbeddedPrincipals_TruncatedFile_IsLoud(t *testing.T) {
	fs := afero.NewMemMapFs()
	// One good entry, then a line past bufio.Scanner's token limit. The
	// scanner returns the first line and then fails with ErrTooLong.
	content := "kept@example.com\n" +
		strings.Repeat("x", bufio.MaxScanTokenSize+1) + "\n" +
		"lost@example.com\n"
	require.NoError(t, afero.WriteFile(fs, paths.DistrustedSignersPath(".ctxloom"), []byte(content), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	suppressed := cfg.SuppressedEmbeddedPrincipals()

	// The fixture must actually truncate from the reader's point of view, or
	// this test proves nothing about the reporting below it.
	require.True(t, suppressed["kept@example.com"], "the entries before the truncation point must load")
	require.False(t, suppressed["lost@example.com"], "the fixture must actually truncate the scan")

	assert.Contains(t, buf.String(), "distrusted_signers",
		"a suppression file that could not be read in full must be reported, naming the file")
	assert.Contains(t, buf.String(), "no first-party signer is trusted this session",
		"the report must name the consequence, not just that something went wrong")

	// And the consequence is real, not merely announced: a revocation below
	// the truncation point cannot be reversed by the truncation.
	assert.False(t,
		cfg.TrustRoot().TrustedForNamespace(parseAuthorizedKey(t, ctxloomReleasePubkey), signing.NamespacePublish, time.Now()).Trusted,
		"a partially-read revocation list must not leave first-party keys trusted")
}

// A malformed LINE in an otherwise-good allowed_signers file is deliberately
// NOT a trust-store finding, and this pins that boundary from both sides.
//
// strictness.ClassTrust is documented as "a corrupt/unreadable trust store
// (the deny-all posture)" — the store as a whole being unusable. Two branches
// of parseAllowedSigners are that (an unreadable file, an unparsable file) and
// both escalate. A skipped line is not: the file opened, parsed, and
// contributed every valid entry it held, which is ssh-keygen's own behaviour
// and the point of TestTrustRoot_MalformedLineSkippedRestStillLoads above.
// Escalating it would abort `ctxloom run` over one stray line while every key
// in the file still works.
//
// It must still be REPORTED — a line that silently does not count is how a
// trusted signer looks revoked — so the warning is asserted here too.
func TestTrustRoot_MalformedLine_WarnsButIsNotATrustStoreFinding(t *testing.T) {
	resetConfigStrictness(t)
	fs := afero.NewMemMapFs()
	pub, keyLine := newTestKey(t)

	content := "this-line-is-garbage-with-no-key\n" +
		"bundles@ctxloom.dev namespaces=\"" + signing.NamespacePublish + "\" " + keyLine
	require.NoError(t, afero.WriteFile(fs, paths.AllowedSignersPath(".ctxloom"), []byte(content), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	mark := strictness.Checkpoint()
	root := cfg.TrustRoot()

	// The fixture must actually contain a line the parser rejects, or the
	// assertions below hold vacuously.
	require.Contains(t, buf.String(), "ignored", "the fixture must produce a per-line parse error")
	assert.True(t, root.TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted,
		"the valid entries in the same file still load")
	assert.Empty(t, strictness.Since(mark),
		"a skipped line is not a corrupt trust store; escalating it would abort startup over one stray line")
}
