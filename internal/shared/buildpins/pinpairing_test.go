package buildpins

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pairing rule of scripts/lint-pins, driven over the REAL pin files: for
// every download the image verifies against a pinned hash or digest, a bump
// of its version alone — the hashes left as they were — must be refused at
// commit time, not discovered as a failed (or, worse, "fixed") image build.

// checksumPinnedTools are the tool-versions.env tools whose downloads the
// Dockerfile checks against `<TOOL>_SHA256_<ARCH>`.
var checksumPinnedTools = []string{"GO", "JUST"}

// realPinRepo is a pinRepo whose HEAD carries the real tree's copy of the pin
// file at rel; it returns the repo and that content.
func realPinRepo(t *testing.T, rel, realPath string) (*pinRepo, string) {
	t.Helper()
	r := newPinRepo(t)
	content := readFile(t, realPath)
	r.stage(rel, content)
	r.git("commit", "-q", "-m", "real pins")
	return r, content
}

// bumpLast increments a dotted version's last numeric component.
func bumpLast(t *testing.T, v string) string {
	t.Helper()
	i := strings.LastIndex(v, ".")
	n, err := strconv.Atoi(v[i+1:])
	require.NoError(t, err, "version %q does not end in a number", v)
	return v[:i+1] + strconv.Itoa(n+1)
}

// replaceOnce replaces old with new exactly once, failing if old is absent.
func replaceOnce(t *testing.T, content, old, new string) string {
	t.Helper()
	require.Contains(t, content, old)
	return strings.Replace(content, old, new, 1)
}

func TestLintPins_RealChecksumPins_VersionBumpAloneIsRefused(t *testing.T) {
	versions := parseToolVersionsEnv(t, toolVersionsPath)
	for _, tool := range checksumPinnedTools {
		t.Run(tool, func(t *testing.T) {
			r, env := realPinRepo(t, ".devcontainer/tool-versions.env", toolVersionsPath)
			key := tool + "_VERSION"
			v := versions[key]
			r.stage(".devcontainer/tool-versions.env", replaceOnce(t, env, key+"="+v+"\n", key+"="+bumpLast(t, v)+"\n"))
			code, out := r.run("")
			assert.Equal(t, 1, code, "a %s bump with its checksums unchanged passed lint-pins:\n%s", key, out)
			for _, arch := range imageArches {
				assert.Contains(t, out, tool+"_SHA256_"+arch)
			}
		})
	}
}
