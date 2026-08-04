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

	"github.com/ctxloom/ctxloom/internal/agents"
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
	cfg := &Config{appPaths: []string{appDir}}

	loader := cfg.SeededBundleLoader()
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
	_ = cfg.SeededBundleLoader()
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
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion hook is included", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })
		result := cfg.ResolveBundleHooks(nil)
		require.Len(t, result.PreTool, 1)
		assert.Equal(t, "ltk evaluate", result.PreTool[0].Command)
		assert.Equal(t, "bundle:"+remote.CompanionSource+"@ltk", result.PreTool[0].SCM)
	})

	t.Run("denying gate withholds it — proves it is NOT the builtin exemption", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return false })
		result := cfg.ResolveBundleHooks(nil)
		assert.Empty(t, result.PreTool, "a companion hook must be withheld by a denying gate — a builtin would NOT be (it's exempt below rejection)")
	})
}

func TestResolveBundleMCPServers_IncludesCompanionLoadoutServers_Gated(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion MCP server is included", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })
		result := cfg.ResolveBundleMCPServers(nil)
		require.Contains(t, result, "ltk-server")
		assert.Equal(t, "bundle:"+remote.CompanionSource+"@ltk", result["ltk-server"].SCM)
	})

	t.Run("denying gate withholds it", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return false })
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
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion command is included with no profile selected", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })
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
		cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return false })
		result := cfg.ResolveBundleCommands(nil)
		assert.Empty(t, result, "a companion command must be withheld by a denying gate — a true builtin would NOT be")
		assert.Empty(t, cfg.ResolveCompanionCommands())
	})
}

func TestResolveBuiltinBundleFragments_IncludesCompanionFragments_Gated(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("trusted gate: companion fragment is included, ref carries the companion source (not builtin:)", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		var seenRef, seenSigner string
		got := cfg.ResolveBuiltinBundleFragments(func(ref string, _ []byte, _, signer string) bool {
			seenRef, seenSigner = ref, signer
			return true
		})
		var found bool
		for _, f := range got {
			if f.Name == remote.CompanionSource+"@ltk#fragments/ltk" {
				found = true
			}
		}
		assert.True(t, found, "companion fragment must be present with the companion's own ref, not a builtin: ref")
		assert.Equal(t, remote.CompanionSource+"@ltk#fragments/ltk", seenRef)
		assert.Empty(t, seenSigner, "this loadout is unsigned")
	})

	t.Run("denying gate withholds it — proves it is NOT the builtin exemption", func(t *testing.T) {
		cfg := &Config{appPaths: []string{appDir}}
		got := cfg.ResolveBuiltinBundleFragments(func(string, []byte, string, string) bool { return false })
		for _, f := range got {
			assert.NotEqual(t, remote.CompanionSource+"@ltk#fragments/ltk", f.Name,
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
		cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })
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
