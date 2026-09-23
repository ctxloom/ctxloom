package companions_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// companionSources is a config.Sources over a fixture Config whose readers
// are exactly what the composition root wires: the project's bundles, the
// builtins and every discovered companion's loadout. The trust ports are the
// test's own fakes, so a verdict can be pinned per item.
type companionSources struct {
	cfg   *config.Config
	ports []compositetest.Option
}

func (s companionSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s companionSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.Trust().Root()
	readers := []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
	}
	return append(readers, companions.Prober{}.ReaderSource()(cfg)...), nil
}

func (s companionSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports(s.ports...)
	return root, records, retraction, nil
}

// rejecting is a human rejection of every item whose "#<kind dir>/<name>"
// tail contains one of denySubstrs; with none, a rejection of nothing.
func rejecting(denySubstrs ...string) compositetest.Option {
	return compositetest.RejectWhen(func(ref trust.Ref, _ []byte) bool {
		tail := "#" + ref.Kind.Dir() + "/" + ref.Name
		for _, s := range denySubstrs {
			if strings.Contains(tail, s) {
				return true
			}
		}
		return false
	})
}

// fakeCompanion puts one companion named bin on the fake PATH, admitted, with
// loadoutYAML as its (unsigned) loadout.
func fakeCompanion(t *testing.T, bin, loadoutYAML string) {
	t.Helper()
	t.Cleanup(companions.AdmitEveryDiscoveredCompanionForTesting())
	t.Cleanup(companions.SetLookPathForTesting(func(name string) (string, error) {
		if name == bin {
			return "/fake/" + bin, nil
		}
		return "", exec.ErrNotFound
	}))
	envelope, err := signing.EncodeLoadoutEnvelope(testsupport.RunLoadout(loadoutYAML), nil, "")
	require.NoError(t, err)
	t.Cleanup(companions.SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil }))
}

func projectWith(t *testing.T, profiles map[string]string, bundlesYAML map[string]string) *config.Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	for name, body := range profiles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(body), 0o644))
	}
	for name, body := range bundlesYAML {
		require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, name+".yaml"), []byte(body), 0o644))
	}
	return config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
		AppPaths:     []string{appDir},
	})
}

// A companion loadout's MCP server rides the SAME gate as a profile-bundle
// server: it comes through when the gate admits it and is withheld when the
// gate names it — never exempt.
func TestCompanionLoadoutMCPServer_RidesTheGenerationsGate(t *testing.T) {
	fakeCompanion(t, "ltk", "version: \"1.0.0\"\nmcp:\n  ltk-server:\n    command: ltk\n    args: [\"serve\"]\n")
	profiles := map[string]string{"dev": "name: dev\nbundles:\n  - mcp-bundle\n"}
	bundlesYAML := map[string]string{"mcp-bundle": "version: \"1.0\"\nmcp:\n  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n"}

	t.Run("admitted", func(t *testing.T) {
		cfg := projectWith(t, profiles, bundlesYAML)
		owner, err := config.Open(context.Background(), companionSources{cfg: cfg})
		require.NoError(t, err)
		result := owner.Current().Config.ResolveBundleMCPServers(nil)
		assert.Contains(t, result, "quiet-server")
		found := false
		for _, srv := range result {
			found = found || strings.HasPrefix(srv.SCM, "bundle:ctxloom+companion:")
		}
		assert.True(t, found, "the companion's MCP server passes the gate like any other")
	})

	t.Run("withheld by name", func(t *testing.T) {
		cfg := projectWith(t, profiles, bundlesYAML)
		owner, err := config.Open(context.Background(), companionSources{cfg: cfg, ports: []compositetest.Option{rejecting("#mcp/ltk-server")}})
		require.NoError(t, err)
		result := owner.Current().Config.ResolveBundleMCPServers(nil)
		assert.Contains(t, result, "quiet-server")
		for name, srv := range result {
			assert.False(t, strings.HasPrefix(srv.SCM, "bundle:ctxloom+companion:"), "companion server %q must be withheld when the gate names it", name)
		}
	})
}

// Companion loadout hooks survive when profile-gated resolution skips
// everything: a missing profile and a ghost bundle contribute nothing, and
// the companion's hook is still there with its companion SCM.
func TestCompanionLoadoutHooks_SurviveSkippedProfiles(t *testing.T) {
	fakeCompanion(t, "taskloom", "version: \"1.0.0\"\nhooks:\n  post_file_edit:\n    - command: ctxloom hook stamp-plan\n      type: command\n")
	cfg := projectWith(t, map[string]string{"real": "name: real\nbundles:\n  - ghost-bundle\n"}, nil)
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{"default": {Profiles: []string{"missing", "real"}}}
	cfg = config.NewFixture(f)

	owner, err := config.Open(context.Background(), companionSources{cfg: cfg})
	require.NoError(t, err)
	result := owner.Current().Config.ResolveBundleHooks(nil)

	found := false
	for _, h := range result.PostFileEdit {
		if strings.Contains(h.Command, "hook stamp-plan") && h.SCM == "bundle:ctxloom+companion:taskloom" {
			found = true
		}
	}
	assert.True(t, found, "companion loadout hooks survive when profile-gated resolution skips everything")
}
