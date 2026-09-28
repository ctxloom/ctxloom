package isolation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// prefixMapper is a TEST-ONLY non-identity pathMapper that maps hostPath to
// prefix+hostPath. It stays INJECTIVE, and works over the host's own temp
// paths, which is what makes it the default mapper of the package's
// fakeRuntime/reapRuntime doubles: a call site that skips the mapper and
// hands back the raw host path produces output a prefixMapper-backed
// assertion can tell apart from the mapped contract — under identity a
// skipped mapper call is byte-identical to a used one.
type prefixMapper struct{ prefix string }

func (m prefixMapper) toContainer(hostPath string) (string, error) { return m.prefix + hostPath, nil }

// windowsDocker is a Docker runtime carrying the Windows host's mapper,
// whatever OS the test runs on.
var windowsDocker = Docker{ociRuntime: ociRuntime{pathMap: driveLetterMapper{}}}

func TestIdentityMapper_NoOp(t *testing.T) {
	got, err := identityMapper{}.toContainer("/home/user/proj")
	require.NoError(t, err)
	assert.Equal(t, "/home/user/proj", got)
}

// A Windows project is mounted from its native path at its drive-mapped
// target, and the run starts there: the SOURCE is the runtime's to
// translate, the target and -w are ours.
func TestBuildRunSpec_DriveLetterMapper_TranslatesWorkDirAndProjectMount(t *testing.T) {
	const hostProj = `C:\Users\ben\my proj`
	spec := runnerSpecFor(windowsDocker, "mock", hostProj, nil, nil)

	assert.Equal(t, "/mnt/c/Users/ben/my proj", spec.WorkDir)
	assert.Contains(t, spec.Mounts, mount{Host: hostProj, Container: "/mnt/c/Users/ben/my proj"})

	argv := strings.Join(Docker{rootless: true}.RunArgs(spec), " ")
	assert.Contains(t, argv, "-w /mnt/c/Users/ben/my proj")
	assert.Contains(t, argv, `source=C:\Users\ben\my proj,target=/mnt/c/Users/ben/my proj`)
	assert.NotContains(t, argv, `target=C:\`, "the target must never carry a Windows path")
}

// exposeMapped routes the target through the SAME mapper the project root
// takes, so the git common-dir mirror and the project never disagree.
func TestOciRuntime_ExposeMapped_RoutesThroughMapper(t *testing.T) {
	assert.Equal(t, mount{Host: `C:\Users\foo\proj\.git`, Container: "/mnt/c/Users/foo/proj/.git", ReadOnly: true},
		exposedMapped(t, windowsDocker, `C:\Users\foo\proj\.git`, true))
}

// relocateRoot under the Windows mapper: the root and its mount are produced
// together from the native path, and a share path is refused by name.
func TestRelocateRoot_DriveLetterMapper(t *testing.T) {
	got, err := relocateRoot(windowsDocker, `C:\p`, "")
	require.NoError(t, err)
	assert.Equal(t, present.Root{Host: `C:\p`, Engine: "/mnt/c/p"}, got.root)
	assert.Equal(t, mount{Host: `C:\p`, Container: "/mnt/c/p"}, got.mount)

	home, err := relocateRoot(windowsDocker, `D:\ctxloom\home`, "/ctxloom/home/.claude")
	require.NoError(t, err)
	assert.Equal(t, mount{Host: `D:\ctxloom\home`, Container: "/ctxloom/home/.claude"}, home.mount,
		"a fixed target is kept; only its source is routed")

	_, err = relocateRoot(windowsDocker, `\\wsl.localhost\Ubuntu\home\u\proj`, "")
	require.ErrorIs(t, err, present.ErrUnreachableRoot)
	require.ErrorIs(t, err, errUNCPath)
}
