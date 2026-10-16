package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkgDir is this package's directory, taken at package init: before any
// test can change the working directory.
var pkgDir, _ = os.Getwd()

// The version the image falls back to when the host's claude cannot be read
// is the tested-version lock's CLAUDE_CODE_CLI_VERSION: the drift workflow
// moves that key, and the fallback must move with it.
func TestTestedVersion_IsTheTestedVersionLock(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "../../../.github/engine-versions.env"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^CLAUDE_CODE_CLI_VERSION=(\S+)$`).FindSubmatch(raw)
	require.NotNil(t, m, "engine-versions.env pins CLAUDE_CODE_CLI_VERSION")
	assert.Equal(t, string(m[1]), testedVersion)
}

// The image never installs whatever npm resolves as latest: the container
// declaration names the tested version, and its plain install is pinned to it.
func TestContainer_InstallsNoUnpinnedClaude(t *testing.T) {
	c, err := Claude{}.Container()
	require.NoError(t, err)
	assert.Equal(t, testedVersion, c.TestedVersion)
	assert.Contains(t, string(c.Install), "npm install -g @anthropic-ai/claude-code@"+testedVersion+" ")
}
