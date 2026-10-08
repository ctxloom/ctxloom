package companions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// --- ProbeCompanionLoadouts: resolve + probe + parse, fail-safe -----------

// lookPathOnly builds a lookPath fake that resolves exactly the given bins
// (to a fixed fake path) and reports every other name as not found —
// including the other registered first-party names these tests pass.
func lookPathOnly(bins map[string]string) func(string) (string, error) {
	return func(bin string) (string, error) {
		if p, ok := bins[bin]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
}

// companionBundles drives the two halves a session drives: the PROBE (which
// execs the registered companions and reads their loadouts) and the READER
// (which parses the bytes). Asserting on the pair is what keeps these tests about the behaviour a
// user gets rather than about either half's internals.
func companionBundles(t *testing.T) map[string]*bundles.Bundle {
	t.Helper()
	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background(), firstPartyCompanions)
	require.NoError(t, err)
	reads, err := bundles.NewCompanionReader(
		func(context.Context) (bundles.CompanionProbe, error) { return probe, nil },
		bundles.WithReaderReporter(strictness.Sink("ctxloom")),
	).Read(context.Background())
	require.NoError(t, err)
	out := make(map[string]*bundles.Bundle, len(reads))
	for _, r := range reads {
		out[r.DisplayName()] = r.Bundle
	}
	return out
}

func TestProbeCompanionLoadouts_NoneOnPathYieldsEmptyMap(t *testing.T) {
	restore := SetLookPathForTesting(lookPathOnly(nil))
	defer restore()

	got := companionBundles(t)
	assert.Empty(t, got)
}

func TestProbeCompanionLoadouts_ProbeFailureSkippedNotCrash(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, exec.ErrNotFound // e.g. a reprise-shaped companion with no `loadout` subcommand yet
	})
	defer restoreProbe()

	got := companionBundles(t)
	assert.Empty(t, got, "a companion whose loadout probe fails contributes nothing, and must not panic")
}

func TestProbeCompanionLoadouts_UnparseableLoadoutWithheldNotCrash(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return []byte("this is not json at all"), nil
	})
	defer restoreProbe()

	got := companionBundles(t)
	assert.Empty(t, got, "an unparseable loadout is withheld, not crashed on")
}

func TestProbeCompanionLoadouts_LoadoutIsSeeded(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	bundleYAML := testsupport.RunLoadout("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	envelope := bundleYAML
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	got := companionBundles(t)
	require.Contains(t, got, remote.CompanionSource+"@ltk")
	b := got[remote.CompanionSource+"@ltk"]
	assert.Contains(t, b.Fragments, "ltk")
}

// --- BundleLoader: companion content sits alongside remote ----------------

// TestBundleLoader_ReadsCompanionAlongsideRemote proves the companion reader's
// content reaches the SAME loader a remote bundle's does, under its
// ctxloom:companion@<bin> ref, and is visible through the loader's normal read
// surface (List/ListAllFragments) exactly like pinned remote content.
func TestBundleLoader_ReadsCompanionAlongsideRemote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	bundleYAML := testsupport.RunLoadout("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	envelope := bundleYAML
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := companionConfig(t, config.Fixture{AppPaths: []string{appDir}, Companions: firstPartyCompanions})

	loader := cfg.BundleLoader()
	infos := loader.List()
	var names []string
	for _, info := range infos {
		names = append(names, info.Name)
	}
	assert.Contains(t, names, remote.CompanionSource+"@ltk")

	frags := loader.ListAllFragments()
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

	cfg := companionConfig(t, config.Fixture{})
	cfg.BundleLoader().List()
	assert.False(t, probed, "no AppPaths means no project to seed companion content into — must not probe at all")
}

// --- Unconditional resolvers pick up companion loadout content ------------
//
// ltk/taskloom content must reach a real session via the companion loadout,
// unconditionally.

const companionLoadoutWithEverything = `
version: "1.0.0"
fragments:
  ltk:
    premise: "You are about to run a shell command this project redirects."
    content: "ltk fragment body"
hooks:
  pre_tool:
    - command: ltk evaluate
      tool: shell
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
	envelope := testsupport.RunLoadout(bundleYAML)
	return func(string) ([]byte, error) { return envelope, nil }
}

func TestResolveBundleHooks_IncludesCompanionLoadoutHooks(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("companion hook is included", func(t *testing.T) {
		cfg := companionConfig(t, config.Fixture{AppPaths: []string{appDir}, Companions: firstPartyCompanions})
		result := cfg.ResolveBundleHooks(nil)
		require.Len(t, result.PreTool, 1)
		assert.Equal(t, "ltk evaluate", result.PreTool[0].Command)
		assert.Equal(t, "bundle:ctxloom+companion:ltk", result.PreTool[0].SCM)
	})

}

func TestResolveBundleMCPServers_IncludesCompanionLoadoutServers(t *testing.T) {
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreProbe := SetCompanionLoadoutOutputForTesting(fakeCompanionEnvelope(t, companionLoadoutWithEverything))
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	t.Run("companion MCP server is included", func(t *testing.T) {
		cfg := companionConfig(t, config.Fixture{AppPaths: []string{appDir}, Companions: firstPartyCompanions})
		result := cfg.ResolveBundleMCPServers(nil)
		require.Contains(t, result, "ltk-server")
		assert.Equal(t, "bundle:ctxloom+companion:ltk", result["ltk-server"].SCM)
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
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
		[]byte("exclude_mcp:\n  - ltk-server\n"), 0o644))

	newCfg := func() *config.Config {
		return companionConfig(t, config.Fixture{
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
			AppPaths:     []string{appDir},
			Companions:   firstPartyCompanions,
		})
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
			[]byte("description: plain\n"), 0o644))
		result := newCfg().ResolveBundleMCPServers([]string{"plain"})
		assert.Contains(t, result, "ltk-server",
			"hoisting the exclusion set must not withhold servers nobody excluded")
	})
}
