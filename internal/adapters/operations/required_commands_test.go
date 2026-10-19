package operations

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/toolchain"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// rustKit requires cargo itself and attaches serena (also required by codenav,
// so the two sources merge), the staged companions and ctxloom, and a remote
// server with no command. Its profile excludes the server named "skipped".
const rustKitYAML = `version: "1.0.0"
requires:
  commands: [cargo, serena]
mcp:
  serena:
    command: serena
    args: ["start-mcp-server"]
  tasks:
    command: taskloom
    args: ["mcp"]
  gate:
    command: ltk
  commit:
    command: reprise
  self:
    command: ctxloom
  skipped:
    command: skipped-tool
  remote:
    url: https://mcp.example.com/sse
profiles:
  dev:
    llm: claude-code
    exclude_mcp: [skipped]
    bundles:
      - ctxloom:local@bundles/rustkit
      - ctxloom:local@bundles/codenav
`

const codeNavYAML = `version: "1.0.0"
requires:
  commands: [serena]
fragments:
  f:
    content: c
`

const rustDevProfile = "ctxloom:local@bundles/rustkit#profiles/dev"

func requiresProject(t *testing.T) *config.Config {
	t.Helper()
	_, cfg := setupProject(t, "claude-code")
	bundleDir := authoredV1(cfg.GetAppPaths()[0])
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	bundletree.WriteOS(t, bundleDir, "rustkit", rustKitYAML)
	bundletree.WriteOS(t, bundleDir, "codenav", codeNavYAML)
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{
		"dev-rust": {Name: "dev-rust", LLM: "claude-code", Profiles: []string{rustDevProfile}, Runtime: "host"},
	}
	return config.NewFixture(f)
}

func TestRequiredCommandsFor_AttributesBundlesAndMCPAndDedupes(t *testing.T) {
	cfg := requiresProject(t)

	got, err := RequiredCommandsFor(context.Background(), engines.Registry(), cfg, "dev-rust")

	require.NoError(t, err)
	assert.Equal(t, []toolchain.Requirement{
		{Command: "cargo", Sources: []toolchain.RequirementSource{
			{Kind: toolchain.KindBundle, Ref: "ctxloom:local@bundles/rustkit"},
		}},
		{Command: "serena", Sources: []toolchain.RequirementSource{
			{Kind: toolchain.KindBundle, Ref: "ctxloom:local@bundles/codenav"},
			{Kind: toolchain.KindBundle, Ref: "ctxloom:local@bundles/rustkit"},
			{Kind: toolchain.KindMCP, Ref: "serena"},
		}},
	}, got, "cargo is attributed to its bundle; serena merges its two bundles and its server")
}

func TestRequiredCommandsFor_ExcludesCtxloomAndTheStagedCompanions(t *testing.T) {
	cfg := requiresProject(t)

	got, err := RequiredCommandsFor(context.Background(), engines.Registry(), cfg, "dev-rust")

	require.NoError(t, err)
	assert.NotContains(t, toolchain.Commands(got), "ctxloom")
	assert.NotContains(t, toolchain.Commands(got), "taskloom")
	assert.NotContains(t, toolchain.Commands(got), "ltk")
	assert.NotContains(t, toolchain.Commands(got), "reprise")
}

func TestRequiredCommandsFor_HonoursExcludeMCP(t *testing.T) {
	cfg := requiresProject(t)

	got, err := RequiredCommandsFor(context.Background(), engines.Registry(), cfg, "dev-rust")

	require.NoError(t, err)
	assert.NotContains(t, toolchain.Commands(got), "skipped-tool", "exclude_mcp withholds the server, so its launcher is not required")
}

func TestDoctorCheckMCPCommands_NamesEachMissingCommandWithItsSource(t *testing.T) {
	cfg := requiresProject(t)
	t.Setenv("PATH", t.TempDir())

	check := doctorCheckMCPCommands(context.Background(), engines.Registry(), cfg, nil, nil)

	assert.Equal(t, DoctorWarn, check.Status, check.Detail)
	assert.Contains(t, check.Detail, "dev-rust: cargo (bundle ctxloom:local@bundles/rustkit), serena (")
	assert.Contains(t, check.Detail, "mcp serena)")
}
