package opencode

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// pkgDir is this package's directory, taken at package init: before
// TestMain moves the working directory into its sandbox.
var pkgDir, _ = os.Getwd()

// fakePins pins every asset releaseSHA256 names, each to a distinct
// well-formed sum (test values, not real hashes).
func fakePins() map[string]string {
	out := map[string]string{}
	i := 0
	for a := range releaseSHA256 {
		i++
		out[a] = strings.Repeat(string(rune('0'+i)), 64)
	}
	return out
}

// The install is the release's own tarball for the pinned version, checked
// against its pinned sha256 BEFORE it is unpacked, and gated by running the
// installed client: every asset the release ships for linux (x64 baseline,
// arm64, and the musl build of each) has an arm carrying its own pin, and an
// unknown machine fails the build by name.
func TestInstallFragment_InstallsThePinnedTarballCheckedAgainstItsSum(t *testing.T) {
	pins := fakePins()
	frag, err := installFragment("1.18.35", pins)
	require.NoError(t, err)
	s := string(frag)

	assert.True(t, strings.HasPrefix(s, "RUN set -e"), "one RUN layer that stops at the first failure")
	assert.Contains(t, s, `curl -fsSL -o "$tmp/opencode.tar.gz" "https://github.com/anomalyco/opencode/releases/download/v1.18.35/$asset.tar.gz"`)
	for a, sum := range pins {
		assert.Contains(t, s, a+") sum="+sum+" ;;", "%s carries its own pin", a)
	}
	assert.Contains(t, s, "x86_64|amd64) asset=opencode-linux-x64-baseline ;;", "x64 takes the baseline build")
	assert.Contains(t, s, "aarch64|arm64) asset=opencode-linux-arm64 ;;")
	assert.Contains(t, s, `asset="$asset-musl"`, "a musl base takes the musl build")
	assert.Contains(t, s, "publishes no linux build for", "an unknown machine fails the build")

	check := strings.Index(s, `sha256sum -c -`)
	unpack := strings.Index(s, `tar -xzf "$tmp/opencode.tar.gz" -C /usr/local/bin opencode`)
	validate := strings.Index(s, "&& opencode --version\n")
	require.True(t, check > 0 && unpack > 0 && validate > 0, "check, unpack and validate are all present:\n%s", s)
	assert.Less(t, check, unpack, "nothing is unpacked before its sum checks")
	assert.Less(t, unpack, validate, "the gate runs the installed client")
	assert.True(t, strings.HasSuffix(s, "\n"), "the fragment ends its last line")
	for _, absent := range []string{"npm", "node", "install.sh", "$HOME", "opencode.ai/install"} {
		assert.NotContains(t, s, absent, "no node, no install script, nothing in HOME")
	}
}

// An asset whose pin is empty or malformed refuses the fragment, naming the
// asset: an install from an unchecked download is never generated.
func TestInstallFragment_RefusesAnUnverifiedPin(t *testing.T) {
	for _, bad := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		pins := fakePins()
		pins["opencode-linux-arm64-musl"] = bad
		_, err := installFragment("1.18.35", pins)
		var unpinned errUnpinned
		require.ErrorAs(t, err, &unpinned, "%q", bad)
		assert.Equal(t, "opencode-linux-arm64-musl", unpinned.asset)
	}
}

// With every pin verified the engine provides a spec that validates; with
// one missing it refuses with ErrUnsupported naming opencode, carrying why,
// so a container binding fails at Resolve.
func TestContainerSpec_ProvidesOnlyWithEveryPin(t *testing.T) {
	spec, err := containerSpec(EngineName, "1.18.35", fakePins())
	require.NoError(t, err)
	require.NoError(t, spec.Validate())
	assert.Equal(t, "opencode --version", spec.ValidateCommand)
	assert.Contains(t, string(spec.Install), "v1.18.35")
	assert.Empty(t, spec.OverlayDirs, "nothing is delivered into the project root")
	assert.Empty(t, spec.InPlaceFiles)

	pins := fakePins()
	pins["opencode-linux-x64-baseline"] = ""
	_, err = containerSpec(EngineName, "1.18.35", pins)
	var unsupported engine.ErrUnsupported
	require.True(t, errors.As(err, &unsupported))
	assert.Equal(t, EngineName, unsupported.Engine)
	assert.Equal(t, "container", unsupported.Capability)
	assert.Contains(t, err.Error(), "opencode-linux-x64-baseline")
}

// The release the image installs is the tested-version lock's
// OPENCODE_CLI_VERSION: the drift workflow moves that key, and the install
// (with its pins) must move with it.
func TestReleaseVersion_IsTheTestedVersionLock(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pkgDir, "../../../.github/engine-versions.env"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^OPENCODE_CLI_VERSION=(\S+)$`).FindSubmatch(raw)
	require.NotNil(t, m, "engine-versions.env pins OPENCODE_CLI_VERSION")
	assert.Equal(t, string(m[1]), releaseVersion)
}
