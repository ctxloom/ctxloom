package opencode

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pkgDir is this package's directory, taken at package init: before
// TestMain moves the working directory into its sandbox.
var pkgDir, _ = os.Getwd()

// The install is the release's own tarball for the pinned version, taken
// from the official GitHub release as-is (upstream publishes no checksums,
// so ctxloom adds none), unpacked, and gated by running the installed
// client: the asset is picked for the machine (x64 baseline, arm64) and the
// libc (the musl build of each), and an unknown machine fails the build by
// name.
func TestInstallFragment_InstallsThePinnedReleaseTarballAsIs(t *testing.T) {
	s := string(installFragment("1.18.35"))

	assert.True(t, strings.HasPrefix(s, "RUN set -e"), "one RUN layer that stops at the first failure")
	assert.Contains(t, s, `for t in curl tar; do`, "the base must carry curl and tar")
	assert.Contains(t, s, `curl -fsSL -o "$tmp/opencode.tar.gz" "https://github.com/anomalyco/opencode/releases/download/v1.18.35/$asset.tar.gz"`)
	assert.Contains(t, s, "x86_64|amd64) asset=opencode-linux-x64-baseline ;;", "x64 takes the baseline build")
	assert.Contains(t, s, "aarch64|arm64) asset=opencode-linux-arm64 ;;")
	assert.Contains(t, s, `asset="$asset-musl"`, "a musl base takes the musl build")
	assert.Contains(t, s, "publishes no linux build for", "an unknown machine fails the build")

	download := strings.Index(s, `curl -fsSL`)
	unpack := strings.Index(s, `tar -xzf "$tmp/opencode.tar.gz" -C /usr/local/bin opencode`)
	validate := strings.Index(s, "&& opencode --version\n")
	require.True(t, download > 0 && unpack > 0 && validate > 0, "download, unpack and validate are all present:\n%s", s)
	assert.Less(t, download, unpack)
	assert.Less(t, unpack, validate, "the gate runs the installed client")
	assert.True(t, strings.HasSuffix(s, "\n"), "the fragment ends its last line")
	for _, absent := range []string{"sha256", "checksum", "sum=", "npm", "node", "install.sh", "$HOME", "opencode.ai/install"} {
		assert.NotContains(t, s, absent, "no checksum step, no node, no install script, nothing in HOME")
	}
}

// The engine always provides its container spec: there is no pin left to be
// missing, so a container binding never refuses on the install.
func TestContainer_ProvidesTheReleaseInstall(t *testing.T) {
	spec, err := Opencode{}.Container()
	require.NoError(t, err)
	require.NoError(t, spec.Validate())
	assert.Equal(t, "opencode --version", spec.ValidateCommand)
	assert.Contains(t, string(spec.Install), "v"+releaseVersion)
	assert.Empty(t, spec.OverlayDirs, "nothing is delivered into the project root")
	assert.Empty(t, spec.InPlaceFiles)
}

// The release the image installs is the tested-version lock's
// OPENCODE_CLI_VERSION: the drift workflow moves that key, and the install
// must move with it.
func TestReleaseVersion_IsTheTestedVersionLock(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "../../../.github/engine-versions.env"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^OPENCODE_CLI_VERSION=(\S+)$`).FindSubmatch(raw)
	require.NotNil(t, m, "engine-versions.env pins OPENCODE_CLI_VERSION")
	assert.Equal(t, string(m[1]), releaseVersion)
}
