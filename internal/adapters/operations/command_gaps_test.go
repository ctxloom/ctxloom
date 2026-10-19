package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// boxKit attaches a stdio server whose binary the image lacks, and a URL
// server that has no command to check.
const boxKitYAML = `version: "1.0.0"
mcp:
  serena:
    command: serena
    args: ["start-mcp-server"]
  remote:
    url: https://mcp.example.com/sse
profiles:
  box:
    llm: claude-code
    bundles:
      - ctxloom:local@bundles/boxkit
`

// hostKit attaches a stdio server whose binary is on the host PATH.
const hostKitYAML = `version: "1.0.0"
mcp:
  hostlocal:
    command: hostlocal-tool
    args: ["--serve"]
profiles:
  hostp:
    llm: claude-code
    bundles:
      - ctxloom:local@bundles/hostkit
`

const (
	boxProfile  = "ctxloom:local@bundles/boxkit#profiles/box"
	hostProfile = "ctxloom:local@bundles/hostkit#profiles/hostp"
)

// gapsProject writes the two kits and a config with one container agent
// ("boxed") and one host agent ("local"). The fake docker is a shell script on
// PATH: it answers image inspect from FAKE_IMAGE_BUILT and runs the probe
// script it is handed under a PATH holding only an empty dir, so the image
// contains exactly nothing the test does not put there, and the real probe
// script (quoting included) is what executes.
func gapsProject(t *testing.T, imageBuilt bool) *config.Config {
	t.Helper()
	_, cfg := setupProject(t, "claude-code")
	bundleDir := authoredV1(cfg.GetAppPaths()[0])
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	bundletree.WriteOS(t, bundleDir, "boxkit", boxKitYAML)
	bundletree.WriteOS(t, bundleDir, "hostkit", hostKitYAML)

	bin, imgbin := t.TempDir(), t.TempDir()
	writeFakeExecutable(t, bin, "hostlocal-tool")
	script := "#!/bin/sh\ncase \"$1\" in\n image) [ -n \"$FAKE_IMAGE_BUILT\" ] || exit 1; echo \"$FAKE_IMAGE_ID\";;\n run) for last; do :; done; PATH=" + imgbin + " exec /bin/sh -c \"$last\";;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	built := ""
	if imageBuilt {
		built = "1"
	}
	t.Setenv("FAKE_IMAGE_BUILT", built)
	// MissingCommands memoizes by image ID for the process, so each test's image
	// needs an ID of its own or an earlier test's answer would stand in for it.
	t.Setenv("FAKE_IMAGE_ID", "sha256:"+t.Name())

	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{
		"boxed": {Name: "boxed", LLM: "claude-code", Profiles: []string{boxProfile}, Runtime: "container-rootless"},
		"local": {Name: "local", LLM: "claude-code", Profiles: []string{hostProfile}, Runtime: "host"},
	}
	return config.NewFixture(f)
}

func TestCommandGapsByAgent_ContainerAgentMissingCommand(t *testing.T) {
	cfg := gapsProject(t, true)

	gaps := CommandGapsByAgent(context.Background(), engines.Registry(), cfg, []isolation.Runtime{isolation.Docker{}})

	assert.Equal(t, map[string][]string{"boxed": {"serena"}}, gaps.Missing,
		"the image lacks serena; the URL server has no command; the host agent's command resolves on PATH")
	assert.Empty(t, gaps.ImageNotBuilt)
}

func TestDoctorCheckMCPCommands_WarnsNamingAgentAndCommand(t *testing.T) {
	cfg := gapsProject(t, true)

	check := doctorCheckMCPCommands(context.Background(), engines.Registry(), cfg, nil, []isolation.Runtime{isolation.Docker{}})

	assert.Equal(t, DoctorWarn, check.Status, check.Detail)
	assert.Contains(t, check.Detail, "boxed -> serena")
	assert.NotContains(t, check.Detail, "local")
	assert.Contains(t, check.Detail, "npx/uvx", "the detail says only the launcher is checked")
	assert.Contains(t, check.Remedy, "isolation_base")
}

func TestDoctorCheckMCPCommands_ImageNotBuiltIsInfo(t *testing.T) {
	cfg := gapsProject(t, false)

	check := doctorCheckMCPCommands(context.Background(), engines.Registry(), cfg, nil, []isolation.Runtime{isolation.Docker{}})

	assert.Equal(t, DoctorInfo, check.Status, check.Detail)
	assert.Contains(t, check.Detail, "boxed")
}

func TestDoctorCheckMCPCommands_HostAgentWithResolvableCommandIsOK(t *testing.T) {
	cfg := gapsProject(t, true)
	f := cfg.ToFixture()
	delete(f.Agents, "boxed")
	cfg = config.NewFixture(f)

	check := doctorCheckMCPCommands(context.Background(), engines.Registry(), cfg, nil, []isolation.Runtime{isolation.Docker{}})

	assert.Equal(t, DoctorOK, check.Status, check.Detail)
}

func TestDoctorCheckMCPCommands_HostAgentMissingCommandWarns(t *testing.T) {
	cfg := gapsProject(t, true)
	t.Setenv("PATH", t.TempDir())
	f := cfg.ToFixture()
	delete(f.Agents, "boxed")
	cfg = config.NewFixture(f)

	check := doctorCheckMCPCommands(context.Background(), engines.Registry(), cfg, nil, nil)

	assert.Equal(t, DoctorWarn, check.Status, check.Detail)
	assert.Contains(t, check.Detail, "local -> hostlocal-tool")
}
