package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

const gapKitYAML = `version: "1.0.0"
mcp:
  serena:
    command: serena
    args: ["start-mcp-server"]
profiles:
  dev:
    llm: claude-code
    bundles:
      - ctxloom:local@bundles/gapkit
`

const gapProfile = "ctxloom:local@bundles/gapkit#profiles/dev"

// gapCreateProject is a project whose one profile attaches the stdio server
// `serena`, with a fake docker on PATH: it answers image inspect from
// FAKE_IMAGE_BUILT and runs the probe script under a PATH holding only
// imageBin, so the image holds exactly what the test puts there. It returns
// imageBin and the host bin dir (also on PATH).
func gapCreateProject(t *testing.T, imageBuilt bool) (imageBin string) {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	bundleDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	bundletree.WriteOS(t, bundleDir, "gapkit", gapKitYAML)
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte("schema_version: 7\n"), 0o644))

	bin, imageBin := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n image) [ -n \"$FAKE_IMAGE_BUILT\" ] || exit 1; echo \"$FAKE_IMAGE_ID\";;\n run) for last; do :; done; PATH=" + imageBin + " exec /bin/sh -c \"$last\";;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	built := ""
	if imageBuilt {
		built = "1"
	}
	t.Setenv("FAKE_IMAGE_BUILT", built)
	// MissingCommands memoizes per image ID for the process.
	t.Setenv("FAKE_IMAGE_ID", "sha256:"+t.Name())
	chdir(t, root)

	prev := theComposition
	theComposition.ContainerRuntimes = func() []isolation.Runtime { return []isolation.Runtime{isolation.Docker{}} }
	t.Cleanup(func() { theComposition = prev })
	return imageBin
}

func putExecutable(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755))
}

// createAgent drives `agent create dev` with args, returning stdout, the info
// stream (cmd stderr) and the warning stream (clidiag's sink).
func createAgent(t *testing.T, args ...string) (stdout, info, warnings string) {
	t.Helper()
	cmd := parseAgentWriteFlags(t, args...)
	resetApp()
	var out, errb, warn bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetContext(context.Background())
	restore := clidiag.SetSink(&warn)
	defer restore()
	require.NoError(t, writeAgentBinding(cmd, "dev", false))
	return out.String(), errb.String(), warn.String()
}

func assertAgentWritten(t *testing.T) {
	t.Helper()
	resetApp()
	cfg, err := GetConfig()
	require.NoError(t, err)
	_, ok := cfg.Agent("dev")
	assert.True(t, ok, "the diagnostic never refuses the write")
}

func TestAgentCreate_ContainerAgentMissingCommandWarnsAndStillWrites(t *testing.T) {
	gapCreateProject(t, true)

	_, info, warnings := createAgent(t, "--llm", "claude-code", "--profiles", gapProfile, "--runtime", "container-rootless")

	assert.Contains(t, warnings, "serena (mcp serena)", "names the command and its source")
	assert.Contains(t, warnings, "isolation_base", "names the remedy")
	assert.Contains(t, warnings, "ctxloom container build")
	assert.NotContains(t, info, "not built")
	assertAgentWritten(t)
}

func TestAgentCreate_ImageNotBuiltIsAnInfoLineAndStillWrites(t *testing.T) {
	gapCreateProject(t, false)

	_, info, warnings := createAgent(t, "--llm", "claude-code", "--profiles", gapProfile, "--runtime", "container-rootless")

	assert.Contains(t, info, "image is not built yet")
	assert.Contains(t, info, "ctxloom container build")
	assert.Contains(t, info, "ctxloom doctor")
	assert.Empty(t, warnings, "an unbuilt image is information, not a gap")
	assertAgentWritten(t)
}

func TestAgentCreate_HostAgentMissingCommandOnPathWarns(t *testing.T) {
	gapCreateProject(t, true)
	// The host may well have a real serena installed; the agent's PATH holds none.
	t.Setenv("PATH", t.TempDir())

	_, _, warnings := createAgent(t, "--llm", "claude-code", "--profiles", gapProfile, "--runtime", "host")

	assert.Contains(t, warnings, "serena (mcp serena)")
	assertAgentWritten(t)
}

func TestAgentCreate_EveryRequiredCommandPresentReportsNoGap(t *testing.T) {
	imageBin := gapCreateProject(t, true)
	putExecutable(t, imageBin, "serena")

	_, info, warnings := createAgent(t, "--llm", "claude-code", "--profiles", gapProfile, "--runtime", "container-rootless")

	assert.Empty(t, warnings)
	assert.NotContains(t, info, "not built")
	assertAgentWritten(t)
}
