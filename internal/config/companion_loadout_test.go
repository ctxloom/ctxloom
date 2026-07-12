package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/remote"
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

func TestProbeCompanionLoadouts_NoneOnPathYieldsEmptyMap(t *testing.T) {
	restore := SetLookPathForTesting(lookPathOnly(nil))
	defer restore()

	got := ProbeCompanionLoadouts(nil)
	assert.Empty(t, got)
}

func TestProbeCompanionLoadouts_ProbeFailureSkippedNotCrash(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, exec.ErrNotFound // e.g. a reprise-shaped companion with no `loadout` subcommand yet
	})
	defer restoreProbe()

	got := ProbeCompanionLoadouts(nil)
	assert.Empty(t, got, "a companion whose loadout probe fails contributes nothing, and must not panic")
}

func TestProbeCompanionLoadouts_UnparseableEnvelopeWithheldNotCrash(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return []byte("this is not json at all"), nil
	})
	defer restoreProbe()

	got := ProbeCompanionLoadouts(nil)
	assert.Empty(t, got, "an unparseable loadout is withheld, not crashed on")
}

func TestProbeCompanionLoadouts_UnsignedLoadoutSeededWithEmptySigner(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	bundleYAML := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	envelope, err := signing.EncodeLoadoutEnvelope(bundleYAML, nil, "")
	require.NoError(t, err)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	got := ProbeCompanionLoadouts(nil)
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

	got := ProbeCompanionLoadouts(root)
	require.Contains(t, got, remote.CompanionSource+"@ltk")
	assert.Equal(t, "ltk@example.com", got[remote.CompanionSource+"@ltk"].Signer())
}

// TestProbeCompanionLoadouts_AdvisorySignerFieldNeverTrusted proves trap #3
// on this surface: an envelope claiming a signer with NO valid signature
// must never be believed, even when that exact principal IS in the trust
// root.
func TestProbeCompanionLoadouts_AdvisorySignerFieldNeverTrusted(t *testing.T) {
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

	got := ProbeCompanionLoadouts(root)
	require.Contains(t, got, remote.CompanionSource+"@ltk")
	assert.Empty(t, got[remote.CompanionSource+"@ltk"].Signer(), "a claimed signer with no signature must never be believed")
}

// --- SeededBundleLoader: companion content merges alongside remote --------

// TestSeededBundleLoader_MergesCompanionAlongsideRemote proves the companion
// seed lands in the SAME seeded-bundle map a remote bundle would, under its
// ctxloom:companion@<bin> ref, and is visible through the loader's normal
// read surface (List/ListAllFragments) exactly like a remote-seeded bundle.
func TestSeededBundleLoader_MergesCompanionAlongsideRemote(t *testing.T) {
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
	cfg := &Config{AppPaths: []string{appDir}}

	loader := cfg.SeededBundleLoader(false)
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

// TestSeededBundleLoader_NoAppPaths_SkipsCompanionProbing proves the guard
// that keeps a bare/management Config (no project directory — the shape
// most unit tests construct) from spawning companion subprocesses at all.
func TestSeededBundleLoader_NoAppPaths_SkipsCompanionProbing(t *testing.T) {
	probed := false
	restoreLook := SetLookPathForTesting(func(string) (string, error) {
		probed = true
		return "", exec.ErrNotFound
	})
	defer restoreLook()

	cfg := &Config{}
	_ = cfg.SeededBundleLoader(false)
	assert.False(t, probed, "no AppPaths means no project to seed companion content into — must not probe at all")
}
