package config

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// --- DiscoverCompanions: first-party UNION ctxloom-companion-* on PATH -----

// TestDiscoverCompanions_UnionsFirstPartyAndPathConvention proves discovery
// is the UNION the spec requires: the shipped first-party list (which does
// NOT match the naming convention) plus every ctxloom-companion-* name found
// scanning $PATH. Neither mechanism alone would find every companion.
func TestDiscoverCompanions_UnionsFirstPartyAndPathConvention(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ctxloom-companion-acme", "ctxloom-companion-widgets", "not-a-companion"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755))
	}
	restorePath := setPathDirsForTesting(t, []string{dir})
	defer restorePath()

	got := DiscoverCompanions()
	assert.Equal(t, []string{
		"ctxloom-companion-acme", "ctxloom-companion-widgets",
		"ltk", "reprise", "taskloom",
	}, got, "sorted union of first-party names and PATH-convention names")
}

// TestDiscoverCompanions_PathConventionDedupesAcrossDirs proves the first
// PATH directory containing a given name wins — mirroring shell PATH
// resolution — rather than the name appearing twice.
func TestDiscoverCompanions_PathConventionDedupesAcrossDirs(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir1, "ctxloom-companion-acme"), []byte("x"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "ctxloom-companion-acme"), []byte("x"), 0o755))
	restorePath := setPathDirsForTesting(t, []string{dir1, dir2})
	defer restorePath()

	got := companionsOnPathByConvention()
	assert.Equal(t, []string{"ctxloom-companion-acme"}, got)
}

// TestDiscoverCompanions_UnreadablePathDirDegradesQuietly proves a PATH
// entry that doesn't exist (a common, ordinary PATH misconfiguration) is
// skipped rather than erroring the whole scan.
func TestDiscoverCompanions_UnreadablePathDirDegradesQuietly(t *testing.T) {
	restorePath := setPathDirsForTesting(t, []string{filepath.Join(t.TempDir(), "does-not-exist")})
	defer restorePath()

	got := DiscoverCompanions()
	assert.Equal(t, []string{"ltk", "reprise", "taskloom"}, got)
}

// setPathDirsForTesting overrides the pathDirs seam so a test can control
// exactly which directories companionsOnPathByConvention scans (readDir
// itself stays the real os.ReadDir — these tests use real temp dirs).
func setPathDirsForTesting(t *testing.T, dirs []string) func() {
	t.Helper()
	prev := pathDirs
	pathDirs = func() []string { return dirs }
	return func() { pathDirs = prev }
}

// --- ProbeCompanionLoadouts: discovery + verify + parse, fail-safe --------

// lookPathOnly builds a lookPath fake that resolves exactly the given bins
// (to a fixed fake path) and reports every other name as not found —
// including the two OTHER first-party names DiscoverCompanions always
// includes, which every test in this file must account for.
func lookPathOnly(bins map[string]string) func(string) (string, error) {
	return func(bin string) (string, error) {
		if p, ok := bins[bin]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
}

// admitEveryDiscoveredCompanion pins the EXEC-CONSENT gate open for tests
// whose subject is what a companion CONTRIBUTES once it runs, not whether it
// was allowed to run at all. Those two questions are answered by different
// code and are worth failing separately: the gate itself is proven in
// companion_consent_test.go, against real files, a real hash and a real
// record. Faking it here also keeps every loadout test from needing an actual
// executable at the fake path lookPath hands back.
func admitEveryDiscoveredCompanion(t *testing.T) {
	t.Helper()
	restore := SetCompanionAdmissionForTesting(func(bins []string, _ bool) []CompanionAdmission {
		out := make([]CompanionAdmission, 0, len(bins))
		for _, bin := range bins {
			path, err := lookPath(bin)
			if err != nil {
				out = append(out, newCompanionAdmission(CompanionKey{Bin: bin}, false, CompanionAdmissionNotInstalled))
				continue
			}
			out = append(out, newCompanionAdmission(
				CompanionKey{Bin: bin, Path: path}, true, CompanionAdmissionConsented))
		}
		return out
	})
	t.Cleanup(restore)
}

// companionBundles drives the two halves a session drives: the PROBE (which
// execs the admitted companions and unwraps their envelopes) and the READER
// (which parses the bytes and establishes what their signature turned out to
// be). Asserting on the pair is what keeps these tests about the behaviour a
// user gets rather than about either half's internals.
func companionBundles(t *testing.T, root signing.TrustRoot) map[string]*bundles.Bundle {
	t.Helper()
	loadouts, err := ProbeCompanionLoadouts(context.Background())
	require.NoError(t, err)
	reads, err := bundles.NewCompanionReader(
		func(context.Context) ([]bundles.CompanionLoadout, error) { return loadouts, nil },
		bundles.WithTrustRoot(root),
	).Read(context.Background())
	require.NoError(t, err)
	out := make(map[string]*bundles.Bundle, len(reads))
	for _, r := range reads {
		out[r.Ref()] = r.Bundle
	}
	return out
}

func TestProbeCompanionLoadouts_NoneOnPathYieldsEmptyMap(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restore := SetLookPathForTesting(lookPathOnly(nil))
	defer restore()

	got := companionBundles(t, nil)
	assert.Empty(t, got)
}

func TestProbeCompanionLoadouts_ProbeFailureSkippedNotCrash(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, exec.ErrNotFound // e.g. a reprise-shaped companion with no `loadout` subcommand yet
	})
	defer restoreProbe()

	got := companionBundles(t, nil)
	assert.Empty(t, got, "a companion whose loadout probe fails contributes nothing, and must not panic")
}

// TestProbeCompanionLoadouts_InvalidSignatureIsReportedNotWithheld pins the
// posture for a signature that FAILS to verify.
//
// This test asserts the OPPOSITE of what an earlier draft did. A publisher
// signature protects bytes from an INTERMEDIARY, and a companion loadout has
// none — the bytes arrive on the stdout of a binary the user already consented
// to execute. So a signature that does not verify here is a stale or mismatched
// signature in the companion's own release: a bug signal, not an attack signal.
// Withholding the loadout would punish the user for the companion's build
// error. The control that catches a SWAPPED companion binary is the hash-keyed
// exec consent, not this.
//
// Two assertions, and the second is the one that matters: the content arrives,
// AND the failure is said out loud. Reporting is what replaces filtering here,
// so a silent admit would be as wrong as a silent drop.
func TestProbeCompanionLoadouts_InvalidSignatureIsReportedNotWithheld(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)

	// Sign one payload, ship a DIFFERENT one under that signature — the exact
	// shape a companion release with a stale .sig produces.
	signed := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: OLD\n")
	shipped := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: NEW\n")
	sig, err := signing.Sign(signed, sshSigner, signing.NamespacePublish)
	require.NoError(t, err)
	envelope, err := signing.EncodeLoadoutEnvelope(shipped, sig, "ltk@example.com")
	require.NoError(t, err)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"ltk@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	})

	var warnings bytes.Buffer
	restoreSink := clidiag.SetSink(&warnings)
	defer restoreSink()

	got := companionBundles(t, root)
	require.Contains(t, got, remote.CompanionSource+"@ltk",
		"a companion's content must still be delivered when its signature does not verify")
	b := got[remote.CompanionSource+"@ltk"]
	assert.Equal(t, "NEW", b.Fragments["ltk"].Content, "the SHIPPED bytes are delivered, not the signed ones")
	assert.Empty(t, b.Signer(), "an unverifiable signature attributes nobody — the content arrives unattributed")

	assert.Contains(t, warnings.String(), "does not verify over its own bytes")
	assert.Contains(t, warnings.String(), "stale or mismatched signature",
		"the warning must name the likely cause a companion author can act on")
	assert.NotContains(t, strings.ToLower(warnings.String()), "tamper",
		"this is a build-error signal, not an attack signal, and must not be phrased as tampering")
}

// TestProbeCompanionLoadouts_UntrustedSignerIsReportedNotWithheld: a valid
// signature by a key this machine does not trust to publish is a FACT about the
// key, not a gate — companion content is admitted at exec.
func TestProbeCompanionLoadouts_UntrustedSignerIsReportedNotWithheld(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	bundleYAML := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	sig, err := signing.Sign(bundleYAML, sshSigner, signing.NamespacePublish)
	require.NoError(t, err)
	envelope, err := signing.EncodeLoadoutEnvelope(bundleYAML, sig, "stranger@example.com")
	require.NoError(t, err)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	var warnings bytes.Buffer
	restoreSink := clidiag.SetSink(&warnings)
	defer restoreSink()

	got := companionBundles(t, nil) // no trust root trusts this key
	require.Contains(t, got, remote.CompanionSource+"@ltk")
	assert.Empty(t, got[remote.CompanionSource+"@ltk"].Signer(), "an untrusted key attributes nobody")
	assert.Contains(t, warnings.String(), "does not trust to publish",
		"the key's trust status is a fact to REPORT, and reporting replaces filtering here")
}

func TestProbeCompanionLoadouts_UnparseableEnvelopeWithheldNotCrash(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return []byte("this is not json at all"), nil
	})
	defer restoreProbe()

	got := companionBundles(t, nil)
	assert.Empty(t, got, "an unparseable loadout is withheld, not crashed on")
}

func TestProbeCompanionLoadouts_UnsignedLoadoutSeededWithEmptySigner(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	bundleYAML := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	envelope, err := signing.EncodeLoadoutEnvelope(bundleYAML, nil, "")
	require.NoError(t, err)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	got := companionBundles(t, nil)
	require.Contains(t, got, remote.CompanionSource+"@ltk")
	b := got[remote.CompanionSource+"@ltk"]
	assert.Empty(t, b.Signer(), "unsigned loadout: empty verified signer, routes to review")
	assert.Contains(t, b.Fragments, "ltk")
}

// TestProbeCompanionLoadouts_SignedByTrustedKeySeededWithPrincipal proves the
// signed loadout -> trusted-signer path end to end: a real ed25519 key,
// trusted for the publish namespace in the caller's root, signs the bundle
// bytes, and the resulting seeded Bundle carries that principal as its
// verified Signer().
func TestProbeCompanionLoadouts_SignedByTrustedKeySeededWithPrincipal(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)

	bundleYAML := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	sig, err := signing.Sign(bundleYAML, sshSigner, signing.NamespacePublish)
	require.NoError(t, err)
	envelope, err := signing.EncodeLoadoutEnvelope(bundleYAML, sig, "ltk@example.com")
	require.NoError(t, err)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"ltk@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	})

	got := companionBundles(t, root)
	require.Contains(t, got, remote.CompanionSource+"@ltk")
	assert.Equal(t, "ltk@example.com", got[remote.CompanionSource+"@ltk"].Signer())
}

// TestProbeCompanionLoadouts_AdvisorySignerFieldNeverTrusted proves trap #3
// on this surface: an envelope claiming a signer with NO valid signature
// must never be believed, even when that exact principal IS in the trust
// root.
func TestProbeCompanionLoadouts_AdvisorySignerFieldNeverTrusted(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"ltk@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	})

	bundleYAML := []byte("version: \"1.0.0\"\n")
	forged := []byte(`{"contract":"ctxloom-loadout/1","bundle":"` + base64.StdEncoding.EncodeToString(bundleYAML) + `","signer":"ltk@example.com"}`)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return forged, nil })
	defer restoreProbe()

	got := companionBundles(t, root)
	require.Contains(t, got, remote.CompanionSource+"@ltk")
	assert.Empty(t, got[remote.CompanionSource+"@ltk"].Signer(), "a claimed signer with no signature must never be believed")
}

// --- BundleLoader: companion content sits alongside remote ----------------

// TestBundleLoader_ReadsCompanionAlongsideRemote proves the companion reader's
// content reaches the SAME loader a remote bundle's does, under its
// ctxloom:companion@<bin> ref, and is visible through the loader's normal read
// surface (List/ListAllFragments) exactly like pinned remote content.
func TestBundleLoader_ReadsCompanionAlongsideRemote(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	t.Setenv("HOME", t.TempDir())
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	bundleYAML := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	envelope, err := signing.EncodeLoadoutEnvelope(bundleYAML, nil, "")
	require.NoError(t, err)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := &Config{appPaths: []string{appDir}}

	loader := cfg.BundleLoader()
	infos, err := loader.List()
	require.NoError(t, err)
	var names []string
	for _, info := range infos {
		names = append(names, info.Name)
	}
	assert.Contains(t, names, remote.CompanionSource+"@ltk")

	frags, err := loader.ListAllFragments()
	require.NoError(t, err)
	found := false
	for _, f := range frags {
		if f.Bundle == remote.CompanionSource+"@ltk" && f.Name == "ltk" {
			found = true
		}
	}
	assert.True(t, found, "the companion's fragment must be visible through the loader's normal listing surface")
}

// TestBundleLoader_NoAppPaths_SkipsCompanionProbing proves the guard that keeps
// a bare/management Config (no project directory — the shape most unit tests
// construct) from spawning companion subprocesses at all.
//
// It READS through the loader rather than only building it: reading is when a
// reader runs, so a test that stopped at construction would pass against a
// loader that probes on its first read.
func TestBundleLoader_NoAppPaths_SkipsCompanionProbing(t *testing.T) {
	probed := false
	restoreLook := SetLookPathForTesting(func(string) (string, error) {
		probed = true
		return "", exec.ErrNotFound
	})
	defer restoreLook()

	cfg := &Config{}
	_, err := cfg.BundleLoader().List()
	require.NoError(t, err)
	assert.False(t, probed, "no AppPaths means no project to seed companion content into — must not probe at all")
}

// --- Unconditional resolvers pick up companion loadout content, GATED -----
//
// These prove item 5's acceptance bar directly: ltk/taskloom content must
// still reach a real session via the companion loadout, unconditionally
// (matching the old embedded-bundle behavior), but — the RED LINE — never
// through the builtin nil-gate exemption. Each test below drives the SAME
// gate a real session would (a plain deny/allow func, not nil), so an
// exemption bug would show up as content leaking through a DENY.

const companionLoadoutWithEverything = `
version: "1.0.0"
fragments:
  ltk:
    content: "ltk fragment body"
hooks:
  pre_tool:
    - command: ltk evaluate
      matcher: Bash
      type: command
mcp:
  ltk-server:
    command: ltk
    args: ["serve"]
commands:
  task-runner:
    description: "Detect and configure the project's task runner"
    content: "ltk task-runner command body"
`

func fakeCompanionEnvelope(t *testing.T, bundleYAML string) func(string) ([]byte, error) {
	t.Helper()
	envelope, err := signing.EncodeLoadoutEnvelope([]byte(bundleYAML), nil, "")
	require.NoError(t, err)
	return func(string) ([]byte, error) { return envelope, nil }
}

func TestResolveBundleHooks_IncludesCompanionLoadoutHooks_Gated(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion hook is included", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(testAuthorizer(true))
		result := cfg.ResolveBundleHooks(nil)
		require.Len(t, result.PreTool, 1)
		assert.Equal(t, "ltk evaluate", result.PreTool[0].Command)
		assert.Equal(t, "bundle:ctxloom+companion:ltk", result.PreTool[0].SCM)
	})

	t.Run("denying gate withholds it — proves it is NOT the builtin exemption", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(testAuthorizer(false))
		result := cfg.ResolveBundleHooks(nil)
		assert.Empty(t, result.PreTool, "a companion hook must be withheld by a denying gate — a builtin would NOT be (it's exempt below rejection)")
	})
}

func TestResolveBundleMCPServers_IncludesCompanionLoadoutServers_Gated(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion MCP server is included", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(testAuthorizer(true))
		result := cfg.ResolveBundleMCPServers(nil)
		require.Contains(t, result, "ltk-server")
		assert.Equal(t, "bundle:ctxloom+companion:ltk", result["ltk-server"].SCM)
	})

	t.Run("denying gate withholds it", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(testAuthorizer(false))
		result := cfg.ResolveBundleMCPServers(nil)
		assert.NotContains(t, result, "ltk-server")
	})
}

// TestResolveBundleCommands_IncludesCompanionLoadoutCommands_Gated proves
// commands get the SAME unconditional-when-present, gated treatment
// ResolveBundleHooks / ResolveBundleMCPServers already have (S8): with no
// profile at all (profileNames nil, no default agent profiles configured), a
// companion's command still resolves through a trusted gate, and a denying
// gate withholds it — proving it is NOT the builtin nil-gate exemption.
func TestResolveBundleCommands_IncludesCompanionLoadoutCommands_Gated(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion command is included with no profile selected", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(testAuthorizer(true))
		result := cfg.ResolveBundleCommands(nil)
		require.Len(t, result, 1)
		assert.Equal(t, "task-runner", result[0].Item)
		assert.Equal(t, remote.CompanionSource+"@ltk", result[0].Bundle)

		companionOnly := cfg.ResolveCompanionCommands()
		require.Len(t, companionOnly, 1)
		assert.Equal(t, "task-runner", companionOnly[0].Item)
	})

	t.Run("denying gate withholds it — proves it is NOT the builtin exemption", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(testAuthorizer(false))
		result := cfg.ResolveBundleCommands(nil)
		assert.Empty(t, result, "a companion command must be withheld by a denying gate — a true builtin would NOT be")
		assert.Empty(t, cfg.ResolveCompanionCommands())
	})
}

func TestResolveBuiltinBundleFragments_IncludesCompanionFragments_Gated(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion fragment is included, ref carries the companion source (not builtin:)", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		var seenRef string
		var seenSignature bundles.Signature
		var seenSigner bundles.Signer
		got := cfg.ResolveBuiltinBundleFragments(bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
			seenRef = e.RefString()
			seenSignature, seenSigner = e.Read.Signature(), e.Read.Signer()
			return bundles.Verdict{Allow: true, Reason: bundles.ReasonCompanion}
		}))
		var found bool
		for _, f := range got {
			if f.Name == "ctxloom+companion:ltk#fragments/ltk" {
				found = true
			}
		}
		assert.True(t, found, "companion fragment must be present with the companion's own ref, not a builtin: ref")
		assert.Equal(t, "ctxloom+companion:ltk#fragments/ltk", seenRef)
		// The read's own axes, not a collapsed signer string: an unsigned
		// loadout reports none/none as a FACT. An empty signer string alone
		// could not: it means both "unsigned" and "signed by a key we
		// distrust" equally.
		assert.Equal(t, bundles.SignatureNone, seenSignature, "this loadout is unsigned")
		assert.Equal(t, bundles.SignerNone, seenSigner, "and so names no key")
	})

	t.Run("denying gate withholds it — proves it is NOT the builtin exemption", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		got := cfg.ResolveBuiltinBundleFragments(testAuthorizer(false))
		for _, f := range got {
			assert.NotEqual(t, "ctxloom+companion:ltk#fragments/ltk", f.Name,
				"a companion fragment must be withheld by a denying gate — a true builtin fragment is exempt and would NOT be")
		}
	})
}

// TestResolveBundleMCPServers_ExcludeMCP_AppliesToCompanionServers is the
// regression guard for a bug where a profile's exclude_mcp could not exclude a
// COMPANION-shipped (or builtin-shipped) MCP server: the `excluded` set was
// built INSIDE the profile-bundle loop, long after the builtin merge and the
// companion loop had already written into `result`. `exclude_mcp: [ltk-server]`
// therefore did precisely nothing, and emitted no diagnostic saying so -- the
// user asked for a server to be withheld, was told nothing, and got it anyway.
//
// The exclusion set is now hoisted out of the profile scope ahead of BOTH
// merges and applied to every write into result, so exclude_mcp means the same
// thing regardless of which source offered the server.
func TestResolveBundleMCPServers_ExcludeMCP_AppliesToCompanionServers(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
		[]byte("name: dev\nexclude_mcp:\n  - ltk-server\n"), 0o644))

	newCfg := func() *Config {
		cfg := &Config{
			defaultAgent: "default",
			agents:       map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
			appPaths:     []string{appDir},
		}
		cfg.SetExecutableTrustGate(testAuthorizer(true))
		return cfg
	}

	t.Run("default profile scope", func(t *testing.T) {
		result := newCfg().ResolveBundleMCPServers(nil)
		assert.NotContains(t, result, "ltk-server",
			"exclude_mcp must withhold a companion-shipped server, not silently ignore the exclusion")
	})

	t.Run("explicitly selected profile scope", func(t *testing.T) {
		result := newCfg().ResolveBundleMCPServers([]string{"dev"})
		assert.NotContains(t, result, "ltk-server",
			"an explicitly passed profile's exclude_mcp must reach the companion merge too")
	})

	t.Run("a profile that excludes nothing still gets the companion server", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "plain.yaml"),
			[]byte("name: plain\n"), 0o644))
		result := newCfg().ResolveBundleMCPServers([]string{"plain"})
		assert.Contains(t, result, "ltk-server",
			"hoisting the exclusion set must not withhold servers nobody excluded")
	})
}
