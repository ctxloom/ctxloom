package isolation

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
)

// junctionHost is a host whose directory links name absolute paths (Windows'
// junctions): they do not resolve inside a container.
type junctionHost struct{ platform.Host }

func (junctionHost) LinksResolveInContainers() bool { return false }

// withHostOS stands h in for the platform for one test.
func withHostOS(t *testing.T, h platform.Host) {
	t.Helper()
	prev := hostOS
	hostOS = h
	t.Cleanup(func() { hostOS = prev })
}

// A container run on a host whose links cannot cross into the container keeps
// its history inside the mounted home: no link is made, native/ is not
// mounted, and the run goes ahead.
func TestNativeHistory_ContainerOnAnUnlinkableHostKeepsHistoryInTheHome(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)

	_, mounts := placeOn(t, s, t.TempDir(), containerOf)

	for _, m := range mounts {
		assert.False(t, strings.Contains(m.Host, string(filepath.Separator)+paths.NativeDirName+string(filepath.Separator)),
			"native/ is not mounted: %s", m.Host)
	}
	_, err := os.Lstat(filepath.Join(claudeHome(home, harpA), claude.TranscriptsDirName))
	assert.ErrorIs(t, err, fs.ErrNotExist, "no link: the engine makes its history dir in the home itself")
}

// The same host still links a HOST run's history into native/.
func TestNativeHistory_HostRunOnAnUnlinkableHostStillLinks(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)

	placeOn(t, s, t.TempDir(), hostRelocator{})

	st, err := os.Lstat(filepath.Join(claudeHome(home, harpA), claude.TranscriptsDirName))
	require.NoError(t, err)
	assert.NotZero(t, st.Mode()&fs.ModeSymlink)
}
